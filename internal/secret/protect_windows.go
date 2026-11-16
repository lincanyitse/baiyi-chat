//go:build windows

package secret

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// protectData 用 DPAPI 保护数据密钥。
//
// DPAPI 绑定当前 Windows 用户：换用户、换机器都解不开。
// 不用 CRYPTPROTECT_LOCAL_MACHINE——那会让同机任何用户都能解密，
// 正好丢掉这个方案的一半价值。
func protectData(plain []byte, _ string) ([]byte, Protection, error) {
	in := windows.DataBlob{
		Size: uint32(len(plain)),
		Data: &plain[0],
	}
	var out windows.DataBlob

	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, "", fmt.Errorf("CryptProtectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))

	return copyBlob(out), ProtectionDPAPI, nil
}

// unprotectData 解开 DPAPI 保护的数据密钥。
func unprotectData(wrapped []byte, _ string) ([]byte, error) {
	if len(wrapped) == 0 {
		return nil, fmt.Errorf("DPAPI 密文为空")
	}
	in := windows.DataBlob{
		Size: uint32(len(wrapped)),
		Data: &wrapped[0],
	}
	var out windows.DataBlob

	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))

	return copyBlob(out), nil
}

// cleanupBinding 在 Windows 上没有绑定文件要清理。
func cleanupBinding(_ string) {}

func copyBlob(b windows.DataBlob) []byte {
	out := make([]byte, b.Size)
	copy(out, unsafe.Slice(b.Data, b.Size))
	return out
}
