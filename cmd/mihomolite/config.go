package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type RuntimeConfig struct {
	APIPort     int
	BackendPort int
	MixedPort   int
	Secret      string
	PanelURL    string
}

func (a *Application) Import(source io.Reader) (string, error) {
	content, err := io.ReadAll(io.LimitReader(source, (4<<20)+1))
	if err != nil {
		return "", err
	}
	if len(content) == 0 || len(content) > 4<<20 {
		return "", errors.New("配置为空或超过 4 MiB")
	}
	if !fileExists(filepath.Join(a.baseDir, "runtime", coreExecutable)) {
		return "", errors.New("尚未安装 mihomo，请先运行 setup.ps1")
	}
	const name = "本地 YAML 配置"
	if err := a.addLocalProfile(name, string(content)); err != nil {
		return "", err
	}
	return name, nil
}

func (a *Application) prepareConfig(content []byte) (RuntimeConfig, []byte, error) {
	return a.prepareProfileConfig(content, "", RuntimeConfig{}, nil, nil)
}

func (a *Application) prepareConfigWithRules(content []byte, runtime RuntimeConfig, custom []CustomRule) (RuntimeConfig, []byte, error) {
	store, err := a.loadProfileStore()
	if err != nil {
		return runtime, nil, err
	}
	return a.prepareProfileConfig(content, store.ActiveID, runtime, custom, nil)
}

// prepareConfigBase receives already merged layers. No persisted profile ID is inferred here.
func (a *Application) prepareConfigBase(content []byte, runtime RuntimeConfig, custom []CustomRule) (RuntimeConfig, []byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return runtime, nil, fmt.Errorf("YAML 格式错误: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return runtime, nil, errors.New("配置必须是 mihomo / Clash YAML 映射，不能直接粘贴 Base64 订阅")
	}
	root, err := materializeConfigRoot(doc.Content[0])
	if err != nil {
		return runtime, nil, err
	}
	doc.Content[0] = root
	if err := enforceLoopbackDNS(root); err != nil {
		return runtime, nil, err
	}
	if value := mappingValue(root, "listeners"); value != nil && !(value.Kind == yaml.SequenceNode && len(value.Content) == 0) {
		return runtime, nil, errors.New("第一版不支持 listeners 入站，请改用 port / mixed-port 配置")
	}
	if value := mappingValue(root, "tunnels"); value != nil && !(value.Kind == yaml.SequenceNode && len(value.Content) == 0) {
		return runtime, nil, errors.New("Lite 不支持 tunnels 额外入站监听，请使用本机 mixed-port")
	}
	ports, webPort := a.runtimePortSnapshot()
	excluded := fixedPortSet(ports)
	excluded[webPort] = true
	for _, port := range []int{runtime.MixedPort, runtime.BackendPort, runtime.APIPort} {
		if port != 0 {
			excluded[port] = true
		}
	}
	mixedPort := runtime.MixedPort
	if mixedPort == 0 {
		mixedPort, err = selectRuntimePort(ports.Proxy, defaultMixedPort, excluded)
		if err != nil {
			return runtime, nil, err
		}
	}
	excluded[mixedPort] = true
	backendPort := runtime.BackendPort
	if backendPort == 0 {
		backendPort, err = selectRuntimePort(ports.Backend, 0, excluded)
		if err != nil {
			return runtime, nil, err
		}
	}
	excluded[backendPort] = true
	apiPort := runtime.APIPort
	if apiPort == 0 {
		apiPort, err = selectRuntimePort(ports.Panel, 19090, excluded)
		if err != nil {
			return runtime, nil, err
		}
	}
	seenPorts := map[int]bool{}
	if webPort != 0 {
		seenPorts[webPort] = true
	}
	for _, port := range []int{mixedPort, backendPort, apiPort} {
		if port < 1 || port > 65535 || seenPorts[port] {
			return runtime, nil, errors.New("运行端口无效或与其它本机端点冲突")
		}
		seenPorts[port] = true
	}
	secret := ""
	a.mu.Lock()
	if a.config != nil {
		secret = a.config.APISecret
	}
	a.mu.Unlock()
	if runtime.Secret != "" {
		secret = runtime.Secret
	}
	if secret == "" {
		var err error
		secret, err = randomSecret()
		if err != nil {
			return runtime, nil, err
		}
	}

	for _, key := range []string{"port", "socks-port", "mixed-port", "redir-port", "tproxy-port", "allow-lan", "bind-address", "external-controller", "external-controller-tls", "external-controller-unix", "external-controller-pipe", "external-ui", "external-ui-name", "external-ui-url", "secret"} {
		deleteMapping(root, key)
	}
	setScalar(root, "mixed-port", strconv.Itoa(mixedPort), "!!int")
	setScalar(root, "allow-lan", "false", "!!bool")
	setScalar(root, "bind-address", "127.0.0.1")
	setScalar(root, "external-controller", "127.0.0.1:"+strconv.Itoa(backendPort))
	setScalar(root, "external-ui", filepath.ToSlash(filepath.Join(a.dataDir, "ui")))
	setScalar(root, "secret", secret)
	if err := ensureUnifiedProxy(root); err != nil {
		return runtime, nil, err
	}
	customRules := custom
	if customRules == nil {
		customRules, err = a.loadCustomRules()
		if err != nil {
			return runtime, nil, err
		}
	}
	if err := mergeCustomRules(root, customRules); err != nil {
		return runtime, nil, err
	}
	setMapping(root, "tun", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "enable"},
		{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"},
	}})
	var rendered bytes.Buffer
	encoder := yaml.NewEncoder(&rendered)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return runtime, nil, err
	}
	if err := encoder.Close(); err != nil {
		return runtime, nil, err
	}
	query := url.Values{}
	query.Set("hostname", "127.0.0.1")
	query.Set("port", strconv.Itoa(apiPort))
	query.Set("secret", secret)
	query.Set("disableUpgradeCore", "1")
	query.Set("disableTunMode", "1")
	runtime = RuntimeConfig{APIPort: apiPort, BackendPort: backendPort, MixedPort: mixedPort, Secret: secret,
		PanelURL: fmt.Sprintf("http://127.0.0.1:%d/ui/#/setup?%s", apiPort, query.Encode())}
	return runtime, rendered.Bytes(), nil
}

func mappingValue(root *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return root.Content[i+1]
		}
	}
	return nil
}
func deleteMapping(root *yaml.Node, key string) {
	for i := 0; i+1 < len(root.Content); {
		if root.Content[i].Value == key {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
		} else {
			i += 2
		}
	}
}
func setScalar(root *yaml.Node, key, value string, tag ...string) {
	t := "!!str"
	if len(tag) > 0 {
		t = tag[0]
	}
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: t, Value: value}
	if existing := mappingValue(root, key); existing != nil {
		*existing = *node
		return
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, node)
}
func setMapping(root *yaml.Node, key string, value *yaml.Node) {
	deleteMapping(root, key)
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// geoCacheFiles are copied into an isolated validation directory so that
// "mihomo -t" never touches (or corrupts) the running core's data directory.
var geoCacheFiles = []string{"geoip.metadb", "GeoSite.dat", "geoip.dat", "ASN.mmdb", "geosite.db"}

func (a *Application) testConfig(content []byte) error {
	return a.testConfigContext(context.Background(), content)
}

func (a *Application) testConfigContext(parent context.Context, content []byte) error {
	return a.testConfigIsolatedContext(parent, content, false)
}

func (a *Application) testConfigOfflineContext(parent context.Context, content []byte) error {
	return a.testConfigIsolatedContext(parent, content, true)
}

func (a *Application) testConfigIsolatedContext(parent context.Context, content []byte, offline bool) error {
	var resources map[string][]byte
	if offline {
		var err error
		content, resources, err = a.offlineValidationInput(content)
		if err != nil {
			return err
		}
	}
	if err := parent.Err(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "mihomolite-validate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for path, data := range resources {
		if err := atomicWrite(filepath.Join(dir, path), data, 0o600); err != nil {
			return err
		}
	}
	for _, name := range geoCacheFiles {
		src := filepath.Join(a.dataDir, name)
		if fileExists(src) {
			if err := copyFile(src, filepath.Join(dir, name)); err != nil {
				return fmt.Errorf("复制 Geo 数据失败: %w", err)
			}
		}
	}
	uiDir := filepath.Join(dir, "ui")
	if err := os.MkdirAll(uiDir, 0o755); err != nil {
		return err
	}
	// prepareConfig points external-ui at data/ui; the isolated working
	// directory only permits its own subpaths, so redirect it here.
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "external-ui:") {
			lines[i] = "external-ui: " + filepath.ToSlash(uiDir)
		}
	}
	content = []byte(strings.Join(lines, "\n"))
	candidate := filepath.Join(dir, "validate.yaml")
	if err := atomicWrite(candidate, content, 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(a.baseDir, "runtime", coreExecutable), "-t", "-d", dir, "-f", candidate)
	cmd.Dir = a.baseDir
	cmd.SysProcAttr = hiddenProcessAttrs()
	output, err := cmd.CombinedOutput()
	message := strings.TrimSpace(string(output))
	if ctx.Err() != nil {
		if parent.Err() != nil {
			return parent.Err()
		}
		return errors.New("配置校验超时，请稍后重试")
	}
	if err != nil {
		if strings.Contains(message, "Can't find MMDB") || strings.Contains(message, "can't download MMDB") {
			return errors.New("此配置包含 GEOIP 规则，但本机缺少 GeoIP 数据库且下载失败。请先连接可用代理后重试，或手动将 geoip.metadb 放入 data 目录")
		}
		if len(message) > 1600 {
			message = message[len(message)-1600:]
		}
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("mihomo 拒绝此配置: %s", message)
	}
	return nil
}

func copyFile(src, dst string) error {
	input, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return atomicWrite(dst, input, 0o600)
}

func availablePort(preferred int) (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(preferred))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return 0, fmt.Errorf("没有可用的本地端口: %w", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
func randomSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// ensureAPISecret keeps the external-controller secret stable across runs so
// the dashboard's saved backend credentials stay valid after reconnect.
func (a *Application) ensureAPISecret() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.config.APISecret != "" {
		return nil
	}
	secret, err := randomSecret()
	if err != nil {
		return err
	}
	next := *a.config
	next.APISecret = secret
	if err := a.saveSettingsValue(next); err != nil {
		return err
	}
	*a.config = next
	return nil
}
