package main

import (
	"archive/zip"
	"debug/pe"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (m *componentUpdateManager) snapshot(c, target string) ([]componentFile, bool, error) {
	if componentRepository(c) == "" {
		return nil, false, errComponentTrust
	}
	if err := componentCheckPath(target); err != nil {
		return nil, false, err
	}
	st, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	result := []componentFile{}
	var total int64
	add := func(path, relative string) error {
		if !componentSafeRelative(relative) || len(result) >= 8192 {
			return errComponentTrust
		}
		info, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > componentMaxExpanded(c) {
			return errComponentTrust
		}
		total += info.Size()
		if total > componentMaxExpanded(c) {
			return errComponentTrust
		}
		h, n, e := componentHash(path)
		if e != nil {
			return e
		}
		result = append(result, componentFile{Path: relative, SHA256: h, Size: n})
		return nil
	}
	if c == "core" {
		if !st.Mode().IsRegular() {
			return nil, false, errComponentTrust
		}
		err = add(target, "mihomo.exe")
	} else {
		if !st.IsDir() {
			return nil, false, errComponentTrust
		}
		entries := 0
		err = filepath.WalkDir(target, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			entries++
			if entries > 16384 {
				return errComponentTrust
			}
			if e := componentCheckPath(path); e != nil {
				return e
			}
			if path == target {
				return nil
			}
			rel, e := filepath.Rel(target, path)
			if e != nil {
				return e
			}
			relative := filepath.ToSlash(rel)
			if !componentSafeRelative(relative) {
				return errComponentTrust
			}
			if d.IsDir() {
				return nil
			}
			return add(path, relative)
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, true, err
}
func componentFilesEqual(a, b []componentFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func componentValidateFiles(c string, files []componentFile, exists bool) error {
	if !exists && len(files) != 0 {
		return errComponentTrust
	}
	if exists && c == "core" && (len(files) != 1 || files[0].Path != "mihomo.exe") {
		return errComponentTrust
	}
	if len(files) > 8192 {
		return errComponentTrust
	}
	var total int64
	seen := map[string]bool{}
	for i, f := range files {
		key := strings.ToLower(f.Path)
		if !componentSafeRelative(f.Path) || !componentSHA256RE.MatchString(f.SHA256) || f.Size < 0 || f.Size > componentMaxExpanded(c) || seen[key] || (i > 0 && files[i-1].Path >= f.Path) {
			return errComponentTrust
		}
		seen[key] = true
		total += f.Size
		if total > componentMaxExpanded(c) {
			return errComponentTrust
		}
	}
	return nil
}
func (m *componentUpdateManager) verify(c, target string, expected []componentFile, exists bool) error {
	files, present, err := m.snapshot(c, target)
	if err != nil {
		return err
	}
	if present != exists || !componentFilesEqual(files, expected) {
		return errors.New("组件文件身份与更新清单不一致，已拒绝替换")
	}
	return nil
}
func componentValidateCore(path string) error {
	binary, err := pe.Open(path)
	if err != nil {
		return errors.New("下载资产不是有效 Windows PE 内核")
	}
	defer binary.Close()
	if binary.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || binary.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 || binary.Characteristics&pe.IMAGE_FILE_DLL != 0 {
		return errComponentTrust
	}
	if _, ok := binary.OptionalHeader.(*pe.OptionalHeader64); !ok {
		return errComponentTrust
	}
	return nil
}
func (m *componentUpdateManager) extract(c, archivePath, payloadRelative string) ([]componentFile, error) {
	if err := componentCheckPath(archivePath); err != nil {
		return nil, err
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, errors.New("组件 ZIP 无效")
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > 8192 {
		return nil, errComponentTrust
	}
	seen := map[string]bool{}
	directories := map[string]bool{}
	var total uint64
	index := ""
	regular := 0
	for _, f := range reader.File {
		name := strings.TrimSuffix(f.Name, "/")
		mode := f.Mode()
		if !componentSafeRelative(name) || mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) || f.Flags&1 != 0 || seen[strings.ToLower(name)] {
			return nil, errComponentTrust
		}
		seen[strings.ToLower(name)] = true
		directories[strings.ToLower(name)] = mode.IsDir()
		if mode.IsDir() {
			if f.UncompressedSize64 != 0 {
				return nil, errComponentTrust
			}
			continue
		}
		regular++
		limit := uint64(32 << 20)
		if c == "core" {
			limit = 128 << 20
		}
		if f.UncompressedSize64 > limit || f.UncompressedSize64 > uint64(componentMaxExpanded(c)) || f.CompressedSize64 > uint64(componentMaxArchive(c)) {
			return nil, errComponentTrust
		}
		if f.UncompressedSize64 > 1<<20 && (f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > 200) {
			return nil, errors.New("组件 ZIP 压缩比异常，拒绝解压")
		}
		total += f.UncompressedSize64
		if total > uint64(componentMaxExpanded(c)) {
			return nil, errComponentTrust
		}
		if name == "index.html" || strings.HasSuffix(name, "/index.html") {
			if index != "" {
				return nil, errComponentTrust
			}
			index = name
		}
	}
	// Reject implicit parent files, case aliases, and file/directory collisions
	// before creating anything. Windows names are treated case-insensitively.
	for _, f := range reader.File {
		name := strings.TrimSuffix(f.Name, "/")
		for parent := filepath.ToSlash(filepath.Dir(name)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if isDir, exists := directories[strings.ToLower(parent)]; exists && !isDir {
				return nil, errComponentTrust
			}
		}
	}
	prefix := ""
	if c == "core" {
		if regular != 1 {
			return nil, errComponentTrust
		}
	} else {
		if index == "" {
			return nil, errors.New("官方面板 ZIP 缺少唯一 index.html")
		}
		if index != "index.html" {
			if index != "dist/index.html" {
				return nil, errComponentTrust
			}
			prefix = "dist/"
		}
		for _, f := range reader.File {
			if prefix != "" && f.Name != "dist/" && !strings.HasPrefix(f.Name, prefix) {
				return nil, errComponentTrust
			}
		}
	}
	payload, err := m.path(payloadRelative)
	if err != nil {
		return nil, err
	}
	if _, err = os.Lstat(payload); !os.IsNotExist(err) {
		return nil, errComponentTrust
	}
	if c == "panel" {
		if _, err = m.mkdir(payloadRelative); err != nil {
			return nil, err
		}
	}
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		name := strings.TrimPrefix(entry.Name, prefix)
		target := payload
		if c == "core" {
			// Official compatible archives contain one top-level mihomo executable.
			if entry.Name != "mihomo.exe" && !(strings.HasPrefix(entry.Name, "mihomo-windows-amd64-compatible-") && strings.HasSuffix(entry.Name, ".exe") && componentTagRE.MatchString(strings.TrimSuffix(strings.TrimPrefix(entry.Name, "mihomo-windows-amd64-compatible-"), ".exe"))) {
				return nil, errComponentTrust
			}
		} else {
			relative := payloadRelative + "/" + name
			if _, err = m.mkdir(filepath.ToSlash(filepath.Dir(relative))); err != nil {
				return nil, err
			}
			target, err = m.path(relative)
			if err != nil {
				return nil, err
			}
		}
		source, e := entry.Open()
		if e != nil {
			return nil, errComponentTrust
		}
		output, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			source.Close()
			return nil, e
		}
		n, e := io.Copy(output, io.LimitReader(source, int64(entry.UncompressedSize64)+1))
		source.Close()
		if e == nil {
			e = output.Sync()
		}
		if closeErr := output.Close(); e == nil {
			e = closeErr
		}
		if e != nil || n != int64(entry.UncompressedSize64) {
			return nil, errors.New("组件 ZIP 内容大小或 CRC 校验失败")
		}
	}
	if c == "core" {
		if err = componentValidateCore(payload); err != nil {
			return nil, err
		}
	} else {
		indexPath, e := m.path(payloadRelative + "/index.html")
		if e != nil {
			return nil, e
		}
		st, e := os.Stat(indexPath)
		if e != nil || st.Size() == 0 {
			return nil, errComponentTrust
		}
	}
	files, _, err := m.snapshot(c, payload)
	return files, err
}
func (m *componentUpdateManager) copyPayload(c, source, destinationRelative string, files []componentFile, exists bool) error {
	if err := m.verify(c, source, files, exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	dest, err := m.path(destinationRelative)
	if err != nil {
		return err
	}
	if _, e := os.Lstat(dest); !os.IsNotExist(e) {
		return errComponentTrust
	}
	if c == "panel" {
		if _, err = m.mkdir(destinationRelative); err != nil {
			return err
		}
	}
	for _, record := range files {
		from := source
		to := dest
		if c == "panel" {
			from = filepath.Join(source, filepath.FromSlash(record.Path))
			if _, err = m.mkdir(filepath.ToSlash(filepath.Dir(destinationRelative + "/" + record.Path))); err != nil {
				return err
			}
			to, err = m.path(destinationRelative + "/" + record.Path)
			if err != nil {
				return err
			}
		}
		if err = componentCheckPath(from); err != nil {
			return err
		}
		input, e := os.Open(from)
		if e != nil {
			return e
		}
		output, e := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			input.Close()
			return e
		}
		n, e := io.Copy(output, io.LimitReader(input, record.Size+1))
		input.Close()
		if e == nil {
			e = output.Sync()
		}
		if ce := output.Close(); e == nil {
			e = ce
		}
		if e != nil || n != record.Size {
			return errComponentTrust
		}
	}
	return m.verify(c, dest, files, true)
}
