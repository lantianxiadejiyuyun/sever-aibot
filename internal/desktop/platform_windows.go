//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func protect(data []byte) ([]byte, error)   { return crypt(data, false) }
func unprotect(data []byte) ([]byte, error) { return crypt(data, true) }
func crypt(data []byte, decrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("密钥为空")
	}
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var output windows.DataBlob
	var err error
	if decrypt {
		err = windows.CryptUnprotectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		err = windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	runtime.KeepAlive(data)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	raw := unsafe.Slice(output.Data, int(output.Size))
	result := append([]byte(nil), raw...)
	clear(raw)
	return result, nil
}

func atomicWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

const startupPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const startupName = "AIBOTClient"

// setStartup only changes this app's current-user Run value. No elevation is
// required, and no credential appears in the command line or registry.
func setStartup(enabled bool) (func() error, error) {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, startupPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, fmt.Errorf("打开登录启动设置: %w", err)
	}
	defer key.Close()
	previous, previousType, getErr := key.GetStringValue(startupName)
	if getErr != nil && !errors.Is(getErr, registry.ErrNotExist) {
		return nil, fmt.Errorf("读取登录启动设置: %w", getErr)
	}
	existed := getErr == nil
	undo := func() error {
		k, e := registry.OpenKey(registry.CURRENT_USER, startupPath, registry.SET_VALUE)
		if e != nil {
			return e
		}
		defer k.Close()
		if existed {
			if previousType == registry.EXPAND_SZ {
				return k.SetExpandStringValue(startupName, previous)
			}
			return k.SetStringValue(startupName, previous)
		}
		e = k.DeleteValue(startupName)
		if errors.Is(e, registry.ErrNotExist) {
			return nil
		}
		return e
	}
	if enabled {
		exe, e := os.Executable()
		if e != nil {
			return nil, e
		}
		err = key.SetStringValue(startupName, `"`+exe+`" --background`)
	} else {
		err = key.DeleteValue(startupName)
		if errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("设置登录启动失败: %w", err)
	}
	return undo, nil
}
