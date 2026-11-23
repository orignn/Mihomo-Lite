//go:build windows

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OPT-001: recovery is private operational state, never configuration/export data.
const proxyRecoveryFile = "proxy-recovery.dat"

var errProxyOwnership = errors.New("系统代理已被其他软件接管或状态不确定；未覆盖当前设置，请在连接诊断中处理")

// Separate DTO: proxySnapshot intentionally has no exported/loggable fields.
type recoverySnapshot struct {
	Enabled       uint32
	Server        string
	Bypass        string
	AutoConfigURL string
	AutoDetect    uint32
}

func snapshotForRecovery(s proxySnapshot) *recoverySnapshot {
	return &recoverySnapshot{s.enabled, s.server, s.bypass, s.autoConfigURL, s.autoDetect}
}
func (s recoverySnapshot) snapshot() proxySnapshot {
	return proxySnapshot{s.Enabled, s.Server, s.Bypass, s.AutoConfigURL, s.AutoDetect}
}

type proxyRecoveryRecord struct {
	Version  int
	Session  string
	Owner    recoveryProcessIdentity
	Core     *recoveryProcessIdentity
	Original *recoverySnapshot
	Applied  *recoverySnapshot
	Phase    string
	Endpoint string // Owned loopback endpoint survives snapshot release until core exit.
}

type recoveryStore interface {
	Load() (*proxyRecoveryRecord, error)
	Save(*proxyRecoveryRecord) error
}
type encryptedRecoveryStore struct{ path string }

func (s encryptedRecoveryStore) checkPath() error {
	for p := s.path; ; p = filepath.Dir(p) {
		attrs, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(p))
		if err == nil && attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("恢复记录路径不能经过重解析点")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("无法核验恢复记录路径")
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
	}
	return nil
}
func (s encryptedRecoveryStore) crypt(data []byte, encrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("恢复记录为空")
	}
	entropy := []byte("MihomoLite/OPT-001/" + strings.ToLower(filepath.Clean(s.path)))
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	extra := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var out windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&in, nil, &extra, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, &extra, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, errors.New("恢复记录加密或解密失败，请使用原 Windows 用户和原安装目录重试（记录已保留）")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
func (s encryptedRecoveryStore) Load() (*proxyRecoveryRecord, error) {
	if err := s.checkPath(); err != nil {
		return nil, err
	}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("无法读取恢复记录")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("恢复记录读取失败或超过大小限制")
	}
	plain, err := s.crypt(data, false)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	var record proxyRecoveryRecord
	dec := json.NewDecoder(bytes.NewReader(plain))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return nil, errors.New("恢复记录格式无效；保留原文件以便诊断")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("恢复记录包含多余数据")
	}
	if err := validateRecoveryRecord(&record); err != nil {
		return nil, err
	}
	return &record, nil
}
func splitRecoveryProxy(server string) (string, int, error) {
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		return "", 0, err
	}
	n, err := strconv.Atoi(port)
	return host, n, err
}
func validateRecoveryRecord(r *proxyRecoveryRecord) error {
	validProcess := func(p recoveryProcessIdentity) bool {
		return validRecoveryProcessIdentity(p) && filepath.IsAbs(p.Image)
	}
	session, err := hex.DecodeString(r.Session)
	if r.Version != 1 || err != nil || len(session) != 16 || !validProcess(r.Owner) || r.Core != nil && !validProcess(*r.Core) {
		return errors.New("恢复记录版本或会话身份无效")
	}
	if (r.Original == nil) != (r.Applied == nil) {
		return errors.New("恢复记录快照不完整")
	}
	if r.Endpoint != "" {
		host, port, err := splitRecoveryProxy(r.Endpoint)
		if err != nil || host != "127.0.0.1" || port < 1 || port > 65535 {
			return errors.New("恢复记录内核代理端点无效")
		}
	}
	if r.Original != nil {
		a := r.Applied.snapshot()
		host, port, err := splitRecoveryProxy(a.server)
		if err != nil || host != "127.0.0.1" || port < 1 || port > 65535 || a.server != r.Endpoint || a.enabled != 1 || a.bypass != windowsProxyBypass || a.autoConfigURL != r.Original.AutoConfigURL || a.autoDetect != r.Original.AutoDetect {
			return errors.New("恢复记录的代理所有权无效")
		}
	}
	if r.Phase != "core" && r.Phase != "prepared" && r.Phase != "applied" && r.Phase != "restoring" {
		return errors.New("恢复记录阶段无效")
	}
	return nil
}
func (s encryptedRecoveryStore) Save(r *proxyRecoveryRecord) error {
	if err := s.checkPath(); err != nil {
		return err
	}
	if r == nil {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("无法清理恢复记录，可重试")
		}
		return nil
	}
	data, err := json.Marshal(r)
	if err != nil {
		return errors.New("无法编码恢复记录")
	}
	defer clear(data)
	encrypted, err := s.crypt(data, true)
	if err != nil {
		return err
	}
	// Explicit Sync before WRITE_THROUGH atomic replacement. Never persist plaintext.
	f, err := os.CreateTemp(filepath.Dir(s.path), ".proxy-recovery-*")
	if err != nil {
		return errors.New("无法暂存恢复记录；未接管系统代理")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(encrypted)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = s.checkPath()
	}
	if err == nil {
		err = replaceFile(tmp, s.path)
	}
	if err != nil {
		return errors.New("无法持久保存恢复记录；已停止后续接管或清理，可重试")
	}
	return nil
}

type proxyRecoveryStatus struct {
	HasRecord    bool
	Pending      bool
	Blocked      bool
	CoreVerified bool
	CorePID      int
	Conflict     bool
	Detail       string
}
type recoverableSystemProxy interface {
	InitializeRecovery(string) error
	TrackCore(recoveryProcessIdentity) error
	ForgetCore() error
	CheckRecoveryBeforeStart() error
	CheckCoreStopSafety() error
	RecoveryStatus() proxyRecoveryStatus
	Recover() error
	KeepCurrentProxy() error
	StopLeftoverCore() error
}

// A legacy PID alone is diagnostic evidence, never authority to terminate.
func (a *Application) checkLegacyCore() error {
	if p, ok := a.proxy.(recoverableSystemProxy); ok && p.RecoveryStatus().HasRecord {
		return nil
	}
	pid, err := readInt(filepath.Join(a.dataDir, "core.pid"))
	if err != nil || pid <= 0 {
		return nil
	}
	identity, err := captureRecoveryProcess(pid)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("旧 PID 记录的进程身份无法核验；请在任务管理器确认，不会自动结束")
	}
	expected, err := filepath.Abs(filepath.Join(a.baseDir, "runtime", coreExecutable))
	if err != nil {
		return errors.New("内核路径无法核验")
	}
	if !strings.EqualFold(filepath.Clean(identity.Image), filepath.Clean(expected)) {
		return nil
	}
	return errors.New("检测到旧版遗留内核，但仅有 PID、缺少会话所有权；请手动核验并退出，不会自动结束")
}

// All record access is guarded by WindowsProxy.mu, like proxy ownership itself.
func (p *WindowsProxy) InitializeRecovery(dataDir string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	path, err := filepath.Abs(filepath.Join(dataDir, proxyRecoveryFile))
	if err != nil {
		return errors.New("恢复记录目录无效")
	}
	p.store = encryptedRecoveryStore{path: path}
	p.legacyCorePath = filepath.Join(filepath.Dir(path), "core.pid")
	if p.checkProcess == nil {
		p.checkProcess = checkRecoveryProcess
	}
	if p.captureProcess == nil {
		p.captureProcess = captureRecoveryProcess
	}
	if p.stopProcess == nil {
		p.stopProcess = stopRecoveryProcess
	}
	return p.loadRecoveryLocked()
}
func (p *WindowsProxy) loadRecoveryLocked() error {
	record, err := p.store.Load()
	if err != nil {
		p.recoveryError = err
		p.lastError = err.Error()
		return err
	}
	p.record = record
	p.recoveryError = nil
	p.foreignSession = record != nil
	p.original = nil
	p.owned = nil
	if record != nil && record.Original != nil {
		original := record.Original.snapshot()
		applied := record.Applied.snapshot()
		p.original = &original
		p.owned = &applied
	}
	return p.recoverLocked()
}
func (p *WindowsProxy) saveRecordLocked(next *proxyRecoveryRecord) error {
	if p.store != nil {
		if next != nil && next.Core == nil && next.Original == nil {
			next = nil
		}
		if err := p.store.Save(next); err != nil {
			p.lastError = err.Error()
			return err
		}
	}
	p.record = next
	return nil
}
func (p *WindowsProxy) newRecordLocked() (*proxyRecoveryRecord, error) {
	owner, err := p.captureProcess(os.Getpid())
	if err != nil {
		return nil, errors.New("无法核验启动器会话身份")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, errors.New("无法生成恢复会话")
	}
	return &proxyRecoveryRecord{Version: 1, Session: hex.EncodeToString(id), Owner: owner, Phase: "core"}, nil
}
func (p *WindowsProxy) ownerGoneLocked() error {
	if p.recoveryError != nil {
		return p.recoveryError
	}
	if p.record == nil || !p.foreignSession {
		return nil
	}
	alive, err := p.checkProcess(p.record.Owner)
	if err != nil {
		return errors.New("无法核验旧启动器身份，未恢复代理或结束内核")
	}
	if alive {
		return errors.New("旧启动器会话仍在运行，未恢复代理或结束内核")
	}
	return nil
}
func (p *WindowsProxy) TrackCore(core recoveryProcessIdentity) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store == nil {
		return nil
	}
	if p.record != nil {
		return errors.New("仍有未处理的恢复会话，请先打开连接诊断")
	}
	r, err := p.newRecordLocked()
	if err != nil {
		return err
	}
	// Use the identity captured by the creating session, never recapture by PID.
	if !validRecoveryProcessIdentity(core) || !filepath.IsAbs(core.Image) {
		return errors.New("无法核验新内核身份")
	}
	r.Core = &core
	p.foreignSession = false
	return p.saveRecordLocked(r)
}
func (p *WindowsProxy) ForgetCore() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.record == nil || p.record.Core == nil {
		return nil
	}
	alive, err := p.checkProcess(*p.record.Core)
	if err != nil || alive {
		return errors.New("内核退出尚未核验，保留恢复记录")
	}
	return p.clearCoreRecordLocked()
}

// The modern record is the only authority for retiring its matching legacy PID
// file. Remove that operational file BEFORE saving away the complete identity,
// so interruption/IO failure leaves durable proof for a safe retry next launch.
func (p *WindowsProxy) clearCoreRecordLocked() error {
	if p.record == nil || p.record.Core == nil {
		return nil
	}
	if err := removeRecoveryCorePIDFile(p.legacyCorePath, p.record.Core.PID); err != nil {
		p.lastError = err.Error()
		return err
	}
	r := *p.record
	r.Core = nil
	if err := p.saveRecordLocked(&r); err != nil {
		return err
	}
	if p.original == nil {
		p.lastError = ""
	}
	return nil
}

// Call only after the captured core instance is gone. A PID is never stop authority.
func removeRecoveryCorePIDFile(path string, expectedPID int) error {
	if path == "" {
		return nil
	}
	safePath := encryptedRecoveryStore{path: path}
	if err := safePath.checkPath(); err != nil {
		return errors.New("无法核验旧 PID 记录路径，保留内核恢复身份")
	}
	pid, err := readInt(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("无法核验旧 PID 记录，保留内核恢复身份")
	}
	if !validRecoveryProcessPID(expectedPID) || pid != expectedPID {
		return nil // Never delete another PID's operational evidence.
	}
	if err := safePath.checkPath(); err != nil {
		return errors.New("无法核验旧 PID 记录路径，保留内核恢复身份")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("无法清理已核验的旧 PID 记录，保留内核恢复身份以便重试")
	}
	return nil
}

func (p *WindowsProxy) CheckRecoveryBeforeStart() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ownerGoneLocked(); err != nil {
		return err
	}
	if p.original != nil {
		return errors.New("存在待处理的系统代理快照，请先打开连接诊断")
	}
	if p.record != nil && p.record.Core != nil {
		alive, err := p.checkProcess(*p.record.Core)
		if err != nil || alive {
			return errors.New("存在遗留内核或身份不确定，请先打开连接诊断")
		}
		if err := p.clearCoreRecordLocked(); err != nil {
			return err
		}
	}
	return nil
}
func (p *WindowsProxy) RecoveryStatus() proxyRecoveryStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := proxyRecoveryStatus{HasRecord: p.record != nil || p.recoveryError != nil, Pending: p.original != nil}
	if err := p.ownerGoneLocked(); err != nil {
		s.Blocked = true
		s.Detail = err.Error()
		return s
	}
	if p.record != nil && p.record.Core != nil && p.foreignSession {
		alive, err := p.checkProcess(*p.record.Core)
		if err != nil {
			s.Blocked = true
			s.Detail = "无法核验遗留内核身份，未结束任何进程"
		} else if alive {
			s.Blocked = true
			s.CoreVerified = true
			s.CorePID = p.record.Core.PID
			s.Detail = "已核验上次会话的遗留内核；需明确确认后结束"
		}
	}
	if s.Pending {
		current, err := p.backend.Read()
		s.Conflict = err == nil && p.owned != nil && current != *p.owned && current != *p.original
		if s.Conflict {
			s.Detail = errProxyOwnership.Error()
		} else if s.Detail == "" {
			s.Detail = "存在连接前的系统代理快照，可安全核验后重试恢复"
		}
	}
	if s.Detail == "" && p.lastError != "" {
		s.Detail = p.lastError
	}
	return s
}
func (p *WindowsProxy) Recover() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.recoveryError != nil {
		return p.loadRecoveryLocked()
	}
	return p.recoverLocked()
}
func (p *WindowsProxy) recoverLocked() error {
	if err := p.ownerGoneLocked(); err != nil {
		p.lastError = err.Error()
		return err
	}
	if err := p.disableLocked(); err != nil {
		return err
	}
	if p.record != nil && p.record.Core != nil {
		alive, err := p.checkProcess(*p.record.Core)
		if err != nil {
			p.lastError = "无法核验遗留内核身份"
			return errors.New(p.lastError)
		}
		if !alive {
			return p.clearCoreRecordLocked()
		}
	}
	return nil
}
func (p *WindowsProxy) KeepCurrentProxy() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ownerGoneLocked(); err != nil {
		return err
	}
	if p.original == nil {
		return nil
	}
	current, err := p.backend.Read()
	if err != nil {
		return errors.New("无法读取当前代理，保留恢复快照")
	}
	if p.owned != nil && proxyUsesRecoveryEndpoint(current, p.owned.server) {
		return errors.New("当前代理仍依赖此内核，请先在 Windows 代理设置中关闭或改离该内核，再保留当前设置")
	}
	if p.owned == nil || current == *p.owned || current == *p.original {
		return errors.New("当前代理未被其他软件接管，请使用恢复原系统代理")
	}
	if p.record != nil {
		r := *p.record
		r.Original = nil
		r.Applied = nil
		r.Phase = "core"
		if err := p.saveRecordLocked(&r); err != nil {
			return err
		}
	}
	p.original = nil
	p.owned = nil
	p.lastError = ""
	return nil
}

func proxyUsesRecoveryEndpoint(s proxySnapshot, endpoint string) bool {
	if s.enabled == 0 || endpoint == "" {
		return false
	}
	// Conservative about WinINet's protocol-specific proxy list and loopback aliases.
	if strings.Contains(strings.ToLower(s.server), strings.ToLower(endpoint)) {
		return true
	}
	_, port, err := splitRecoveryProxy(endpoint)
	if err != nil {
		return true
	}
	for _, entry := range strings.Split(s.server, ";") {
		value := strings.TrimSpace(entry)
		if _, tail, found := strings.Cut(value, "="); found {
			value = strings.TrimSpace(tail)
		}
		for _, prefix := range []string{"http://", "https://", "socks://", "socks5://"} {
			value = strings.TrimPrefix(value, prefix)
		}
		host, p, err := splitRecoveryProxy(value)
		if err == nil && p == port && (strings.EqualFold(host, "localhost") || host == "::1" || strings.HasPrefix(host, "127.")) {
			return true
		}
	}
	return false
}
func (p *WindowsProxy) CheckCoreStopSafety() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.checkCoreStopSafetyLocked()
}
func (p *WindowsProxy) checkCoreStopSafetyLocked() error {
	if p.record == nil || p.record.Endpoint == "" {
		return nil
	}
	current, err := p.backend.Read()
	if err != nil {
		return errors.New("停核前无法核验当前代理依赖，保留内核与恢复记录")
	}
	if proxyUsesRecoveryEndpoint(current, p.record.Endpoint) {
		return errors.New("当前系统代理仍依赖此内核，未结束进程；请先恢复代理或在 Windows 关闭/改离该代理")
	}
	return nil
}

func (p *WindowsProxy) StopLeftoverCore() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ownerGoneLocked(); err != nil {
		return err
	}
	if !p.foreignSession {
		return errors.New("不是遗留会话，请使用正常断开")
	}
	// Never break an owned proxy by stopping its only surviving core first.
	if err := p.disableLocked(); err != nil {
		return err
	}
	if p.record == nil || p.record.Core == nil {
		return nil
	}
	if err := p.checkCoreStopSafetyLocked(); err != nil {
		return err
	}
	if err := p.stopProcess(*p.record.Core); err != nil {
		return errors.New("遗留内核未结束：身份已变化、权限不足或等待退出失败")
	}
	return p.clearCoreRecordLocked()
}
