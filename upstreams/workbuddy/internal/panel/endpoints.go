// endpoints.go 面板「接入地址」接口：把当前网关可被访问到的地址一次性算好，
// 供配置页展示与一键复制。
//
// 为什么放后端算：内网穿透的公网地址写在 frpc.toml 里（serverAddr + remotePort），
// 只有进程所在机器能读到；前端无法自行推导。同时本机/局域网地址需要知道真实
// 网卡 IP（listen 为 :7863 时监听全部网卡，但前端只知道浏览器当前用的那个 host）。
package panel

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// AccessEndpoint 一条接入地址。
type AccessEndpoint struct {
	Label string `json:"label"` // 展示名，如「本机」「局域网」「公网穿透」
	Base  string `json:"base"`  // OpenAI 兼容 Base URL，如 http://127.0.0.1:7863/v1
	Root  string `json:"root"`  // 根地址（面板用），如 http://127.0.0.1:7863
	Note  string `json:"note"`  // 附加说明（可能为空）
}

// accessEndpoints 计算接入地址列表。
//
// listen 形如 ":7863" / "127.0.0.1:7863" / "0.0.0.0:7863"；据此决定要不要给出
// 局域网地址（只听 127.0.0.1 时给局域网地址是误导，故不给）。
// frpc.toml 存在且能解析出 serverAddr/remotePort 时才给公网地址。
func (p *Panel) accessEndpoints(listen string) []AccessEndpoint {
	port := listenPort(listen)
	bindAll := bindsAllInterfaces(listen)

	var out []AccessEndpoint
	out = append(out, AccessEndpoint{
		Label: "本机",
		Base:  "http://127.0.0.1:" + port + "/v1",
		Root:  "http://127.0.0.1:" + port,
		Note:  "仅本机可访问",
	})

	if bindAll {
		for _, ip := range lanIPv4s() {
			out = append(out, AccessEndpoint{
				Label: "局域网",
				Base:  "http://" + ip + ":" + port + "/v1",
				Root:  "http://" + ip + ":" + port,
				Note:  "同一局域网内的设备可访问",
			})
		}
	}

	if addr, remotePort, ok := p.frpcEndpoint(); ok {
		out = append(out, AccessEndpoint{
			Label: "公网穿透",
			Base:  "http://" + addr + ":" + remotePort + "/v1",
			Root:  "http://" + addr + ":" + remotePort,
			Note:  "由 frpc.toml 自动读取；需 frpc 正在运行",
		})
	}

	return out
}

// accessHandler GET /panel/api/endpoints
func (p *Panel) accessHandler(w http.ResponseWriter, r *http.Request) {
	listen := ""
	if p.cfg.LoadConfig != nil {
		if cfg, err := p.cfg.LoadConfig(); err == nil {
			listen = extractListen(cfg)
		}
	}
	if listen == "" {
		listen = ":7863" // 与 config 默认值一致，避免前端拿到空串
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"listen":    listen,
		"endpoints": p.accessEndpoints(listen),
	})
}

// extractListen 从 LoadConfig 返回的任意配置对象里取 listen 字段。
// 配置对象由 main 注入（具体类型在 cmd/server），这里用反射式读取避免反向依赖：
// 优先走 map（json 解码结果），其次走结构体字段。
func extractListen(cfg any) string {
	if m, ok := cfg.(map[string]any); ok {
		if s, ok := m["listen"].(string); ok {
			return s
		}
	}
	// 结构体兜底：过一遍 JSON 再取键最稳（配置对象都很小），
	// 避免为读一个字段而反向依赖 cmd/server 的具体类型。
	if b, err := json.Marshal(cfg); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			if s, ok := m["listen"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// listenPort 取 listen 的端口部分，缺省 7863。
func listenPort(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return "7863"
	}
	return port
}

// bindsAllInterfaces 判断 listen 是否绑定全部网卡
// （空 host、0.0.0.0、:: 都算）。
func bindsAllInterfaces(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return true
	}
	return false
}

// lanIPv4s 返回本机非回环、非链路本地的 IPv4 地址。
func lanIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip4.String())
		}
	}
	return out
}

var (
	reFrpcServerAddr = regexp.MustCompile(`(?m)^\s*serverAddr\s*=\s*"([^"]+)"`)
	reFrpcRemotePort = regexp.MustCompile(`(?m)^\s*remotePort\s*=\s*(\d+)`)
)

// frpcEndpoint 从 frpc.toml 读取公网地址。
//
// 只读、尽力而为：文件不存在或字段缺失时返回 false（配置页就不显示公网地址），
// 绝不因解析失败而报错——穿透属于可选外部设施，网关不依赖它。
// 搜索顺序：进程工作目录 → 可执行文件所在目录。
func (p *Panel) frpcEndpoint() (addr, port string, ok bool) {
	for _, dir := range candidateDirs() {
		raw, err := os.ReadFile(filepath.Join(dir, "frpc.toml"))
		if err != nil {
			continue
		}
		text := string(raw)
		mAddr := reFrpcServerAddr.FindStringSubmatch(text)
		mPort := reFrpcRemotePort.FindStringSubmatch(text)
		if len(mAddr) < 2 || len(mPort) < 2 {
			continue
		}
		if _, err := strconv.Atoi(mPort[1]); err != nil {
			continue
		}
		return strings.TrimSpace(mAddr[1]), mPort[1], true
	}
	return "", "", false
}

// candidateDirs 返回查找 frpc.toml 的候选目录（去重、保序）。
func candidateDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(d string) {
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	if wd, err := os.Getwd(); err == nil {
		add(wd)
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Dir(exe))
	}
	return dirs
}
