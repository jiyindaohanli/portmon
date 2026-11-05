# PortMon

Windows 11 端口占用监控 + 安全结束进程。Go + Wails v2，单 exe，约 12MB，无运行时依赖。

界面是卡通风（粗描边 / 硬阴影 / 弹簧动效），但数据区保持等宽数字和高行高——可爱归可爱，端口号不能看不清。

## 它能做什么

- 实时列出所有**正在监听**的 TCP / UDP 端口（IPv4 + IPv6），显示占用进程、命令行、所属用户、父进程、启动时间、内存
- **WSL2 桥接**：Docker 容器 / WSL 内部服务发布到宿主机的端口也看得见（`127.0.0.1:16389 -> 6379` 这种会显示成容器名 + 镜像）
- **盯守端口**：指定几个端口（3306/6379/315…），固定展示它们当前被谁占用；空闲的也占一行，状态一眼可见
- 按端口 / 进程名 / PID / 绑定地址搜索
- 头部实时显示整机 **CPU / 内存 / 磁盘** 占用率；鼠标悬停看明细（内存已用/可用，磁盘逐盘符占用与总量）
- 手动结束进程（单个或整棵进程树），结束后**回查端口是否真的释放**
- 系统关键进程只给看、不给杀，理由写在界面上（不是一句"无权限"）
- 托盘常驻：关闭窗口收进托盘，托盘菜单可暂停监听 / 开关开机自启 / 退出

## 快速开始

```powershell
# 一次性：装 Wails CLI（需要 Go 1.26+）
go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.1

# 构建
wails build
# 产物：build\bin\PortMon.exe
```

首次构建会自动生成 `frontend/wailsjs`（Wails 绑定，仅给 IDE 提示用，前端代码不 import 它）。

## 目录

```
main.go                     窗口 / 托盘 / 提权 helper 入口
app.go                      绑定给前端的 API
internal/win32/
  table.go                  GetExtendedTcpTable / GetExtendedUdpTable（P/Invoke）
  process.go                Toolhelp 进程表 + NtQueryInformationProcess 命令行 + 令牌用户
  guard.go                  危险分级：kernel / critical / system / risky / wsl
  kill.go                   结束进程 + 进程树 + 端口释放回查
  elevate.go                runas 提权 helper（临时文件回传结果）
  autostart.go              HKCU Run 注册表自启
internal/monitor/poller.go  自适应轮询 + 变化签名
internal/wsl/wsl.go         WSL2 桥接：ss + docker ps（低频后台探测）
internal/store/config.go    %APPDATA%\PortMon\config.json（盯守端口）
internal/sysinfo/sysinfo.go CPU / 内存 / 磁盘采样（GetSystemTimes / GlobalMemoryStatusEx / GetDiskFreeSpaceEx）
frontend/dist/              静态前端（无构建步骤）：index.html / style.css / app.js
tools/genicon/main.go       图标生成器（生成 appicon.png / icon.ico / logo.png）
```

## 几个必须知道的技术决定

**不用 `netstat`，也不用 `Get-NetTCPConnection`。** 实测：`Get-NetTCPConnection -State Listen` 单次约 1980ms（1Hz 轮询会把界面卡死），`netstat -ano` 265ms 且每次都拉起子进程，而 `GetExtendedTcpTable` 只要 1~3ms。`golang.org/x/sys/windows` 没有导出这个函数，所以 `table.go` 里是手写的 P/Invoke。两个坑：端口字段是**网络字节序**，地址字段也是；IPv6 必须单独走 `MIB_TCP6ROW_OWNER_PID`，否则只监听 `::1` 的服务看不见。

**"实时"是 250ms 轮询，不是事件驱动。** Windows 没有端口表变更的回调 API（iphlpapi 只提供 `NotifyIpInterfaceChange` / `NotifyRouteChange2`，管的是接口和路由）。所以：窗口聚焦 250ms 采样，失焦 1500ms，托盘 1.5s。真正避免界面空转的是**变化签名**——`poller.go` 把整份视图做 SHA1，签名没变就不往前端推，签名变了才推全量（约 150 行，几十 KB）。

**WSL2 的端口必须在虚拟机内部查。** WSL2 的 localhost 转发是内核级重定向，**不会**在 Windows TCP 表里留下 LISTEN 行——`netstat`、TCPView、Process Explorer 都看不到某个 Docker 容器的 `127.0.0.1:16389`，但你 `Test-NetConnection` 又能连通。所以 `internal/wsl` 用一次 `wsl.exe -e sh -c "ss -ltn; docker ps"` 同时拿端口和容器映射，合并成 `source=wsl` 的行。这些行没有宿主 PID，所以只能看不能杀。

**探测成本决定它必须是低频的。** 单次 `wsl.exe` 调用 150~400ms，不能塞进 250ms 的快采样；而且没开 WSL 时根本不该去拉进程。所以它跑在独立 goroutine，8 秒一轮，且只在进程里看到 `vmmemWSL` / `wslservice` 时才真正执行。

**结束进程后必须回查。** 进程一死它的端口行就消失了,所以"查这个 PID 还有没有在监听"恒为真、等于没查。正确做法是结束**之前**记下它监听的 `地址:端口`,结束后轮询这些端口是否已经完全没有监听者。这就是 `KillResult.Freed` 的含义。

**谁能杀谁不能杀。** 三档硬拦截：PID 0/4（内核本身，杀 = 蓝屏，端口 80/443 还会额外提示可能属于 http.sys 的 URL 预留）；`IsProcessCritical` 返回真的进程（PPL/关键进程，内核会直接拒绝并触发重启）；`csrss/wininit/winlogon/smss/lsass/services` 等核心组件。`svchost/dwm/explorer` 这类归为"可杀但要二次确认"，弹窗里说明后果。WSL2/Docker 的端口代理（`wslrelay/vmmemWSL`）单独标出来，因为杀宿主进程解决不了问题。

**提权不做常驻服务。** 普通权限运行时遇到 `ERROR_ACCESS_DENIED`，弹出"以管理员身份重试"：用 `ShellExecuteW("runas")` 拉起带 `-kill -pid <n> -result <tmp>` 参数的自身，helper 干完活把 JSON 写进临时文件就退出，主进程轮询取回。没有 IPC、没有服务、没有安装器。

## 已知边界

- 非管理员看不到其他用户进程的命令行和工作目录（详情面板会明确说明）
- 系统保护进程永远杀不掉，这是设计而不是缺陷
- WSL 桥接拿不到虚拟机内的进程名和 PID（无免密 sudo），只能给容器名；非 Docker 的 WSL 服务显示为“WSL 内部服务”
- WSL 里的端口只能看、不能从宿主机结束
- 看不到“谁正在通过这个端口通信”的对端进程（需要 WFP 驱动级能力）
- UDP 没有连接状态，只显示绑定

## 改图标

`tools/genicon/main.go` 用超椭圆 + 圆形画怪物脸，自带 4x 超采样抗锯齿。改完跑：

```powershell
go run ./tools/genicon .
wails build
```
