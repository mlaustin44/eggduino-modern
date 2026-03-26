package main

import (
	"embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.bug.st/serial"
)

var Version = "dev"

//go:embed eggduino-web.html
var htmlFS embed.FS

// --- Serial port management ---

type SerialConn struct {
	mu   sync.Mutex
	port serial.Port
}

func (s *SerialConn) Connect(portName string, baud int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.port != nil {
		s.port.Close()
		s.port = nil
	}

	log.Printf("Opening %s @ %d...", portName, baud)
	mode := &serial.Mode{BaudRate: baud}
	p, err := serial.Open(portName, mode)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", portName, err)
	}
	s.port = p

	// Wait for Arduino reset (opening the port toggles DTR)
	log.Println("Waiting for Arduino boot...")
	time.Sleep(2500 * time.Millisecond)

	// Drain any garbage from bootloader
	log.Println("Draining boot garbage...")
	s.port.SetReadTimeout(200 * time.Millisecond)
	drain := make([]byte, 256)
	for {
		n, _ := s.port.Read(drain)
		if n == 0 {
			break
		}
		log.Printf("  drained %d bytes", n)
	}

	// Query version
	log.Println("Sending version query...")
	s.port.SetReadTimeout(1 * time.Second)
	lines, err := s.sendLocked("v")
	log.Printf("Version response: %v (err=%v)", lines, err)
	if err != nil {
		s.port.Close()
		s.port = nil
		return "", fmt.Errorf("version query failed: %w", err)
	}

	version := ""
	for _, l := range lines {
		if strings.Contains(l, "EBB") || strings.Contains(l, "Egg") || strings.Contains(l, "V1.") {
			version = l
			break
		}
	}
	if version == "" && len(lines) > 0 {
		version = lines[0]
	}

	// Set a reasonable read timeout for normal commands
	s.port.SetReadTimeout(5 * time.Second)

	log.Printf("Connected: %s", version)
	return version, nil
}

func (s *SerialConn) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port != nil {
		s.port.Close()
		s.port = nil
		log.Println("Disconnected")
	}
}

func (s *SerialConn) Send(cmd string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port == nil {
		return nil, fmt.Errorf("not connected")
	}
	return s.sendLocked(cmd)
}

func (s *SerialConn) sendLocked(cmd string) ([]string, error) {
	_, err := s.port.Write([]byte(cmd + "\r"))
	if err != nil {
		return nil, err
	}

	// Read response lines until we see "OK" or "unknown CMD" or timeout
	var lines []string
	buf := make([]byte, 256)
	var acc string
	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		n, err := s.port.Read(buf)
		if n > 0 {
			acc += string(buf[:n])
			for {
				idx := strings.Index(acc, "\r\n")
				if idx < 0 {
					break
				}
				line := acc[:idx]
				acc = acc[idx+2:]
				if line == "" {
					continue
				}
				// DBG lines are diagnostic — log them server-side and include in response
				if strings.HasPrefix(line, "DBG") {
					log.Printf("  [arduino] %s", line)
				}
				lines = append(lines, line)
				// "OK" or error terminates the response
				if line == "OK" || strings.HasPrefix(line, "unknown") {
					return lines, nil
				}
			}
		}
		// Read timeout (no data) — if we already have lines, return them.
		// This handles commands like "v" that don't send "OK".
		if err != nil || n == 0 {
			if len(lines) > 0 {
				return lines, nil
			}
			// Keep waiting until deadline
		}
	}

	// Deadline reached
	if len(lines) > 0 || acc != "" {
		if acc != "" {
			lines = append(lines, strings.TrimSpace(acc))
		}
		return lines, nil
	}
	return nil, fmt.Errorf("timeout waiting for response to: %s", cmd)
}

// --- Auto-detect Arduino serial port ---

func findArduino() string {
	ports, err := serial.GetPortsList()
	if err != nil {
		return ""
	}
	for _, p := range ports {
		lower := strings.ToLower(p)
		if strings.Contains(lower, "acm") || strings.Contains(lower, "usbmodem") {
			return p
		}
	}
	for _, p := range ports {
		lower := strings.ToLower(p)
		if strings.Contains(lower, "usb") || strings.Contains(lower, "serial") {
			return p
		}
	}
	if len(ports) > 0 {
		return ports[0]
	}
	return ""
}

// --- WebSocket handler ---

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wsMsg struct {
	Action string `json:"action"`
	Cmd    string `json:"cmd,omitempty"`
	Port   string `json:"port,omitempty"`
}

type wsResp struct {
	OK      bool     `json:"ok"`
	Action  string   `json:"action"`
	Error   string   `json:"error,omitempty"`
	Lines   []string `json:"lines,omitempty"`
	Version string   `json:"version,omitempty"`
	Ports   []string `json:"ports,omitempty"`
}

func wsHandler(sc *SerialConn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("ws upgrade:", err)
			return
		}
		defer conn.Close()
		log.Println("WebSocket client connected")

		for {
			var msg wsMsg
			if err := conn.ReadJSON(&msg); err != nil {
				log.Println("ws read:", err)
				break
			}
			log.Printf("ws recv: %+v", msg)

			var resp wsResp
			resp.Action = msg.Action

			switch msg.Action {
			case "connect":
				portName := msg.Port
				if portName == "" {
					portName = findArduino()
				}
				if portName == "" {
					resp.Error = "No serial port found. Plug in the Arduino."
				} else {
					ver, err := sc.Connect(portName, 115200)
					if err != nil {
						resp.Error = err.Error()
					} else {
						resp.OK = true
						resp.Version = ver
					}
				}

			case "disconnect":
				sc.Disconnect()
				resp.OK = true

			case "send":
				lines, err := sc.Send(msg.Cmd)
				if err != nil {
					resp.Error = err.Error()
				} else {
					resp.OK = true
					resp.Lines = lines
				}

			case "ports":
				ports, _ := serial.GetPortsList()
				resp.OK = true
				resp.Ports = ports

			default:
				resp.Error = "unknown action: " + msg.Action
			}

			log.Printf("ws send: ok=%v err=%q lines=%v", resp.OK, resp.Error, resp.Lines)
			conn.WriteJSON(resp)
		}
	}
}

// --- Open browser ---

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	}
	if cmd != nil {
		cmd.Start()
	}
}

// --- Main ---

func main() {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	sc := &SerialConn{}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := htmlFS.ReadFile("eggduino-web.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	http.HandleFunc("/ws", wsHandler(sc))

	url := "http://" + addr
	arduino := findArduino()

	fmt.Printf("EggDuino %s\n", Version)
	fmt.Printf("  Web UI:  %s\n", url)
	if arduino != "" {
		fmt.Printf("  Arduino: %s\n", arduino)
	} else {
		fmt.Println("  Arduino: not detected (plug in and click Connect)")
	}
	fmt.Println("  Press Ctrl+C to quit")
	fmt.Println()

	go func() {
		time.Sleep(200 * time.Millisecond)
		openBrowser(url)
	}()

	log.Fatal(http.ListenAndServe(addr, nil))
}
