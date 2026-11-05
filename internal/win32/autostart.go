package win32

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	autostartKey = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartVal = "PortMon"
)

// AutostartEnabled 读取 HKCU Run 项判断是否已开机自启。
func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autostartVal)
	if err != nil || v == "" {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(v), strings.ToLower(exe))
}

// SetAutostart 写入或删除 HKCU Run 项。写 HKCU 不需要管理员权限。
func SetAutostart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if !enable {
		if err := k.DeleteValue(autostartVal); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// -autostart 让界面直接收进托盘，不要开机弹窗
	return k.SetStringValue(autostartVal, `"`+exe+`" -autostart`)
}
