package main

import (
	"encoding/binary"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getExtendedTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

// loopbackListeners reports IPv4 TCP listeners that can serve 127.0.0.1.
// Inspect ownership without opening connections to unrelated local services.
func loopbackListeners() (map[int][]int, error) {
	const ownerPIDListener = 3 // TCP_TABLE_OWNER_PID_LISTENER
	size := uint32(0)
	var table []byte
	for attempt := 0; attempt < 4; attempt++ {
		var ptr uintptr
		if len(table) > 0 {
			ptr = uintptr(unsafe.Pointer(&table[0]))
		}
		code, _, _ := getExtendedTCPTable.Call(ptr, uintptr(unsafe.Pointer(&size)), 0, windows.AF_INET, ownerPIDListener, 0)
		runtime.KeepAlive(table)
		switch windows.Errno(code) {
		case 0:
			return parseLoopbackListeners(table)
		case windows.ERROR_INSUFFICIENT_BUFFER:
			if size >= 4 && size <= 16<<20 {
				table = make([]byte, size)
				continue
			}
		}
		return nil, fmt.Errorf("读取 Windows TCP 监听表失败: %w", windows.Errno(code))
	}
	return nil, fmt.Errorf("Windows TCP 监听表正在变化，请重新检查")
}

func parseLoopbackListeners(table []byte) (map[int][]int, error) {
	const rowSize = 24 // MIB_TCPROW_OWNER_PID consists of six DWORDs.
	if len(table) < 4 || uint64(binary.LittleEndian.Uint32(table[:4])) > uint64((len(table)-4)/rowSize) {
		return nil, fmt.Errorf("Windows TCP 监听表格式无效")
	}
	count := int(binary.LittleEndian.Uint32(table[:4]))
	listeners := make(map[int][]int)
	for i := 0; i < count; i++ {
		row := table[4+i*rowSize:][:rowSize]
		if addr := binary.LittleEndian.Uint32(row[4:8]); addr != 0 && addr != 0x0100007f {
			continue
		}
		port := int(binary.BigEndian.Uint16(row[8:10]))
		pid := int(binary.LittleEndian.Uint32(row[20:24]))
		listeners[port] = append(listeners[port], pid)
	}
	return listeners, nil
}

func listenerOwnedBy(pids []int, pid int) bool {
	for _, owner := range pids {
		if owner != pid {
			return false
		}
	}
	return pid > 0 && len(pids) > 0
}
