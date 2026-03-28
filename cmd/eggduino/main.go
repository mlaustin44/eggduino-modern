package main

import (
	"embed"
	"flag"
	"fmt"
	"log"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.bug.st/serial"
)

var Version = "dev"

//go:embed eggduino-web.html
var htmlFS embed.FS

// --- Backend interface ---

type Backend interface {
	Connect(port string, baud int) (string, error)
	Disconnect()
	Send(cmd string) ([]string, error)
}

// === Real serial backend ===

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

	log.Println("Waiting for Arduino boot...")
	time.Sleep(2500 * time.Millisecond)

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

	s.port.SetReadTimeout(30 * time.Second)
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

	var lines []string
	buf := make([]byte, 256)
	var acc string
	deadline := time.Now().Add(30 * time.Second)

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
				if strings.HasPrefix(line, "DBG") {
					log.Printf("  [arduino] %s", line)
				}
				lines = append(lines, line)
				if line == "OK" || strings.HasPrefix(line, "unknown") {
					return lines, nil
				}
			}
		}
		if err != nil || n == 0 {
			if len(lines) > 0 {
				return lines, nil
			}
		}
	}

	log.Printf("timeout on: %s — draining serial buffer", cmd)
	s.port.SetReadTimeout(200 * time.Millisecond)
	drainBuf := make([]byte, 256)
	for {
		n, _ := s.port.Read(drainBuf)
		if n == 0 {
			break
		}
		log.Printf("  drained %d bytes after timeout", n)
	}
	s.port.SetReadTimeout(30 * time.Second)

	if len(lines) > 0 || acc != "" {
		if acc != "" {
			lines = append(lines, strings.TrimSpace(acc))
		}
		return lines, nil
	}
	return nil, fmt.Errorf("timeout waiting for response to: %s", cmd)
}

// === Mock backend ===

type MockBackend struct {
	mu         sync.Mutex
	connected  bool
	penPos     int // current pen position in steps
	rotPos     int // current rotation position in steps
	penIsUp    bool
	motorsOn   bool
	penUpPos   int
	penDownPos int
	penLimNeg  int
	penLimPos  int
	limitsOn   bool

	// Bitmap canvas
	img      *image.RGBA
	imgW     int
	imgH     int
	penColor color.RGBA
	travelColor color.RGBA

	// Stats
	totalPenSteps   int // absolute sum
	totalRotSteps   int // absolute sum
	moveCount       int
	minPen          int
	maxPen          int
	minRot          int
	maxRot          int
	drawPenSteps    int // pen-down absolute movement
	drawRotSteps    int // pen-down absolute movement
	drawMinRot      int
	drawMaxRot      int
	drawMinPen      int
	drawMaxPen      int
	drawMoveCount   int
}

func NewMockBackend() *MockBackend {
	return &MockBackend{
		penIsUp:    true,
		penUpPos:   5,
		penDownPos: 20,
		penLimNeg:  -1640,
		penLimPos:  1100,
		limitsOn:   true,
	}
}

func (m *MockBackend) Connect(port string, baud int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = true
	m.penPos = 0
	m.rotPos = 0
	m.penIsUp = true
	m.motorsOn = false
	m.totalPenSteps = 0
	m.totalRotSteps = 0
	m.moveCount = 0
	m.minPen = 0
	m.maxPen = 0
	m.minRot = 0
	m.maxRot = 0
	m.drawMoveCount = 0
	m.drawPenSteps = 0
	m.drawRotSteps = 0
	m.drawMinRot = 0
	m.drawMaxRot = 0
	m.drawMinPen = 0
	m.drawMaxPen = 0

	// Create bitmap: width = 3200 (rotation), height = pen travel range
	m.imgW = 3200
	m.imgH = m.penLimPos - m.penLimNeg
	m.img = image.NewRGBA(image.Rect(0, 0, m.imgW, m.imgH))
	// Fill with white background
	for y := 0; y < m.imgH; y++ {
		for x := 0; x < m.imgW; x++ {
			m.img.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	m.penColor = color.RGBA{0, 0, 0, 255}       // black for pen-down drawing
	m.travelColor = color.RGBA{200, 200, 255, 255} // light blue for pen-up travel

	log.Printf("[mock] Connected — bitmap %dx%d", m.imgW, m.imgH)
	return "EBBv13_and_above Protocol emulated by MockEggDuino", nil
}

func (m *MockBackend) Disconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = false
	m.printStats()
	m.saveImage()
	log.Println("[mock] Disconnected")
}

func (m *MockBackend) Send(cmd string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.connected {
		return nil, fmt.Errorf("not connected")
	}
	return m.handle(cmd)
}

func (m *MockBackend) handle(cmd string) ([]string, error) {
	parts := strings.SplitN(cmd, ",", -1)
	op := parts[0]

	switch op {
	case "v":
		return []string{"EBBv13_and_above Protocol emulated by MockEggDuino"}, nil

	case "EM":
		if len(parts) >= 2 {
			val, _ := strconv.Atoi(parts[len(parts)-1])
			m.motorsOn = val == 1
			log.Printf("[mock] motors %s", map[bool]string{true: "ON", false: "OFF"}[m.motorsOn])
		}
		return []string{"OK"}, nil

	case "SM":
		if len(parts) >= 4 {
			dur, _ := strconv.Atoi(parts[1])
			penSteps, _ := strconv.Atoi(parts[2])
			rotSteps, _ := strconv.Atoi(parts[3])

			// Draw line on bitmap
			oldPen := m.penPos
			oldRot := m.rotPos
			m.penPos += penSteps
			m.rotPos += rotSteps

			if m.img != nil {
				c := m.travelColor
				if !m.penIsUp {
					c = m.penColor
				}
				m.drawLine(oldRot, oldPen, m.rotPos, m.penPos, c)
			}

			m.totalPenSteps += int(math.Abs(float64(penSteps)))
			m.totalRotSteps += int(math.Abs(float64(rotSteps)))
			m.moveCount++

			if m.penPos < m.minPen {
				m.minPen = m.penPos
			}
			if m.penPos > m.maxPen {
				m.maxPen = m.penPos
			}
			if m.rotPos < m.minRot {
				m.minRot = m.rotPos
			}
			if m.rotPos > m.maxRot {
				m.maxRot = m.rotPos
			}

			if !m.penIsUp {
				m.drawPenSteps += int(math.Abs(float64(penSteps)))
				m.drawRotSteps += int(math.Abs(float64(rotSteps)))
				m.drawMoveCount++
				if m.penPos < m.drawMinPen { m.drawMinPen = m.penPos }
				if m.penPos > m.drawMaxPen { m.drawMaxPen = m.penPos }
				if m.rotPos < m.drawMinRot { m.drawMinRot = m.rotPos }
				if m.rotPos > m.drawMaxRot { m.drawMaxRot = m.rotPos }
			}

			log.Printf("[mock] SM dur=%d pen=%d rot=%d → penPos=%d rotPos=%d (pen %s)",
				dur, penSteps, rotSteps, m.penPos, m.rotPos,
				map[bool]string{true: "UP", false: "DOWN"}[m.penIsUp])
		}
		return []string{"OK"}, nil

	case "SP":
		if len(parts) >= 2 {
			val, _ := strconv.Atoi(parts[1])
			m.penIsUp = val == 0
			log.Printf("[mock] pen %s at penPos=%d rotPos=%d",
				map[bool]string{true: "UP", false: "DOWN"}[m.penIsUp], m.penPos, m.rotPos)
		}
		return []string{"OK"}, nil

	case "SC":
		return []string{"OK"}, nil

	case "QP":
		state := "0"
		if m.penIsUp {
			state = "1"
		}
		return []string{state, "OK"}, nil

	case "QT":
		return []string{fmt.Sprintf("%d,%d", m.penLimNeg, m.penLimPos), "OK"}, nil

	case "QD":
		s := fmt.Sprintf("DBG motors=%s penPos=%d rotPos=%d limits=%s penLimNeg=%d penLimPos=%d",
			map[bool]string{true: "ON", false: "OFF"}[m.motorsOn],
			m.penPos, m.rotPos,
			map[bool]string{true: "ON", false: "OFF"}[m.limitsOn],
			m.penLimNeg, m.penLimPos)
		return []string{s, "OK"}, nil

	case "HM":
		m.penPos = 0
		m.rotPos = 0
		log.Println("[mock] home set")
		return []string{"OK"}, nil

	case "GH":
		log.Printf("[mock] go home from penPos=%d rotPos=%d", m.penPos, m.rotPos)
		m.penPos = 0
		m.rotPos = 0
		return []string{"OK"}, nil

	case "TL":
		if len(parts) >= 2 {
			val, _ := strconv.Atoi(parts[1])
			m.limitsOn = val == 1
		}
		return []string{"OK"}, nil

	case "PL":
		if len(parts) >= 3 {
			a, _ := strconv.Atoi(parts[1])
			b, _ := strconv.Atoi(parts[2])
			m.penLimNeg = min(a, b)
			m.penLimPos = max(a, b)
			log.Printf("[mock] limits set %d to %d", m.penLimNeg, m.penLimPos)
		}
		return []string{"OK"}, nil

	default:
		log.Printf("[mock] unknown command: %s", cmd)
		return []string{"unknown CMD"}, nil
	}
}

// drawLine uses Bresenham's algorithm. Coordinates are in step space:
// x = rotation (0 to imgW), y = pen (penLimNeg to penLimPos, mapped to 0..imgH)
func (m *MockBackend) drawLine(x0, y0, x1, y1 int, c color.RGBA) {
	// Map pen position to image Y: penLimNeg → 0, penLimPos → imgH
	iy0 := y0 - m.penLimNeg
	iy1 := y1 - m.penLimNeg
	// Wrap rotation to image X
	ix0 := ((x0 % m.imgW) + m.imgW) % m.imgW
	ix1 := ((x1 % m.imgW) + m.imgW) % m.imgW

	// Bresenham
	dx := ix1 - ix0
	if dx < 0 { dx = -dx }
	dy := iy1 - iy0
	if dy < 0 { dy = -dy }
	sx := 1
	if ix0 > ix1 { sx = -1 }
	sy := 1
	if iy0 > iy1 { sy = -1 }
	err := dx - dy

	steps := dx + dy
	if steps == 0 { steps = 1 }
	for i := 0; i <= steps; i++ {
		px := ((ix0 % m.imgW) + m.imgW) % m.imgW
		if px >= 0 && px < m.imgW && iy0 >= 0 && iy0 < m.imgH {
			m.img.SetRGBA(px, iy0, c)
		}
		if ix0 == ix1 && iy0 == iy1 { break }
		e2 := 2 * err
		if e2 > -dy { err -= dy; ix0 += sx }
		if e2 < dx { err += dx; iy0 += sy }
	}
}

func (m *MockBackend) saveImage() {
	if m.img == nil { return }
	exePath, _ := os.Executable()
	imgPath := filepath.Join(filepath.Dir(exePath), "eggduino-mock-plot.png")
	f, err := os.Create(imgPath)
	if err != nil {
		log.Printf("[mock] failed to save image: %v", err)
		return
	}
	defer f.Close()
	png.Encode(f, m.img)
	log.Printf("[mock] Plot image saved: %s", imgPath)
}

func (m *MockBackend) printStats() {
	log.Println("[mock] === Plot Statistics ===")
	log.Printf("[mock]   Total moves: %d (pen-down: %d)", m.moveCount, m.drawMoveCount)
	log.Printf("[mock]   ALL  pen range: %d to %d (span: %d)", m.minPen, m.maxPen, m.maxPen-m.minPen)
	log.Printf("[mock]   ALL  rot range: %d to %d (span: %d)", m.minRot, m.maxRot, m.maxRot-m.minRot)
	log.Printf("[mock]   DRAW pen range: %d to %d (span: %d)", m.drawMinPen, m.drawMaxPen, m.drawMaxPen-m.drawMinPen)
	log.Printf("[mock]   DRAW rot range: %d to %d (span: %d)", m.drawMinRot, m.drawMaxRot, m.drawMaxRot-m.drawMinRot)
	log.Printf("[mock]   DRAW rot as %% of 3200: %.1f%%", float64(m.drawMaxRot-m.drawMinRot)/3200*100)
	log.Printf("[mock]   Total pen travel: %d abs steps (draw: %d)", m.totalPenSteps, m.drawPenSteps)
	log.Printf("[mock]   Total rot travel: %d abs steps (draw: %d)", m.totalRotSteps, m.drawRotSteps)
	log.Printf("[mock]   Final position: pen=%d rot=%d", m.penPos, m.rotPos)
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
	Action   string         `json:"action"`
	Cmd      string         `json:"cmd,omitempty"`
	Port     string         `json:"port,omitempty"`
	Paths    [][][2]float64 `json:"paths,omitempty"`
	Settings *PlotSettings  `json:"settings,omitempty"`
}

type wsResp struct {
	OK       bool           `json:"ok"`
	Action   string         `json:"action"`
	Error    string         `json:"error,omitempty"`
	Lines    []string       `json:"lines,omitempty"`
	Version  string         `json:"version,omitempty"`
	Ports    []string       `json:"ports,omitempty"`
	Paths    [][][2]float64 `json:"paths,omitempty"`
	Progress *float64       `json:"progress,omitempty"`
	PosX     *float64       `json:"posX,omitempty"`
	PosY     *float64       `json:"posY,omitempty"`
}

func wsHandler(be Backend, plotter *Plotter, mock bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("ws upgrade:", err)
			return
		}
		defer conn.Close()
		log.Println("WebSocket client connected")

		// Mutex for writing to websocket from multiple goroutines
		var wsMu sync.Mutex
		writeJSON := func(v interface{}) {
			wsMu.Lock()
			defer wsMu.Unlock()
			conn.WriteJSON(v)
		}

		for {
			var msg wsMsg
			if err := conn.ReadJSON(&msg); err != nil {
				log.Println("ws read:", err)
				break
			}
			if msg.Action != "send" {
				log.Printf("ws recv: action=%s", msg.Action)
			}

			var resp wsResp
			resp.Action = msg.Action

			switch msg.Action {
			case "connect":
				if mock {
					ver, err := be.Connect("mock", 0)
					if err != nil {
						resp.Error = err.Error()
					} else {
						resp.OK = true
						resp.Version = ver
					}
				} else {
					portName := msg.Port
					if portName == "" {
						portName = findArduino()
					}
					if portName == "" {
						resp.Error = "No serial port found. Plug in the Arduino."
					} else {
						ver, err := be.Connect(portName, 115200)
						if err != nil {
							resp.Error = err.Error()
						} else {
							resp.OK = true
							resp.Version = ver
						}
					}
				}

			case "disconnect":
				if plotter.IsRunning() {
					plotter.Stop()
				}
				be.Disconnect()
				resp.OK = true

			case "send":
				lines, err := be.Send(msg.Cmd)
				if err != nil {
					resp.Error = err.Error()
				} else {
					resp.OK = true
					resp.Lines = lines
				}

			case "ports":
				if mock {
					resp.OK = true
					resp.Ports = []string{"mock"}
				} else {
					ports, _ := serial.GetPortsList()
					resp.OK = true
					resp.Ports = ports
				}

			case "optimize":
				polys := wireToPoly(msg.Paths)
				pathsBefore := len(polys)
				totalPtsBefore := 0
				for _, p := range polys {
					totalPtsBefore += len(p)
				}

				polys = OptimizePaths(polys, 1.0, 0.5, 1.0)

				totalPtsAfter := 0
				outPaths := polyToWire(polys)
				for _, p := range polys {
					totalPtsAfter += len(p)
				}

				log.Printf("optimize: %d paths/%d pts → %d paths/%d pts",
					pathsBefore, totalPtsBefore, len(outPaths), totalPtsAfter)
				resp.OK = true
				resp.Paths = outPaths

			case "plot":
				if plotter.IsRunning() {
					resp.Error = "plot already in progress"
					break
				}
				if msg.Settings == nil || len(msg.Paths) == 0 {
					resp.Error = "missing paths or settings"
					break
				}

				// Convert and optimize paths
				polys := wireToPoly(msg.Paths)
				polys = OptimizePaths(polys, 1.0, 0.5, 1.0)
				log.Printf("[plot] optimized: %d → %d paths", len(msg.Paths), len(polys))

				// Get pen limits
				penLimNeg, penLimPos := -400, 200 // defaults
				limLines, limErr := be.Send("QT")
				if limErr == nil && len(limLines) > 0 {
					parts := strings.SplitN(limLines[0], ",", 2)
					if len(parts) == 2 {
						n, e1 := strconv.Atoi(parts[0])
						p, e2 := strconv.Atoi(parts[1])
						if e1 == nil && e2 == nil {
							penLimNeg = min(n, p)
							penLimPos = max(n, p)
						}
					}
				}

				// Start plot in background, stream progress
				ch := plotter.Run(be, polys, *msg.Settings, 3200, 800, penLimNeg, penLimPos)
				resp.OK = true

				// Send initial response
				writeJSON(resp)

				// Stream progress updates
				go func() {
					for prog := range ch {
						var update wsResp
						update.Action = "progress"
						update.OK = true
						update.Progress = &prog.Progress
						update.PosX = &prog.PosX
						update.PosY = &prog.PosY
						if prog.Done {
							done := true
							_ = done
							if prog.Error != "" {
								update.Error = prog.Error
							}
						}
						writeJSON(update)
					}
				}()
				continue // don't send resp again

			case "pause":
				plotter.Pause()
				resp.OK = true

			case "resume":
				plotter.Resume()
				resp.OK = true

			case "stop":
				plotter.Stop()
				resp.OK = true

			default:
				resp.Error = "unknown action: " + msg.Action
			}

			writeJSON(resp)
		}
	}
}

func wireToPoly(paths [][][2]float64) []Polyline {
	polys := make([]Polyline, len(paths))
	for i, path := range paths {
		polys[i] = make(Polyline, len(path))
		for j, pt := range path {
			polys[i][j] = Point{pt[0], pt[1]}
		}
	}
	return polys
}

func polyToWire(polys []Polyline) [][][2]float64 {
	out := make([][][2]float64, len(polys))
	for i, poly := range polys {
		out[i] = make([][2]float64, len(poly))
		for j, pt := range poly {
			out[i][j] = [2]float64{pt.X, pt.Y}
		}
	}
	return out
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
	mockFlag := flag.Bool("mock", false, "Run with mock EBB backend (no hardware needed)")
	flag.Parse()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	// Log to file in mock mode
	if *mockFlag {
		exePath, _ := os.Executable()
		logPath := filepath.Join(filepath.Dir(exePath), "eggduino-mock.log")
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err == nil {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
			fmt.Printf("  Log:     %s\n", logPath)
		}
	}

	var be Backend
	if *mockFlag {
		be = NewMockBackend()
	} else {
		be = &SerialConn{}
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := htmlFS.ReadFile("eggduino-web.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	plt := NewPlotter()
	http.HandleFunc("/ws", wsHandler(be, plt, *mockFlag))
	http.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		log.Println("Quit requested from web UI")
		w.WriteHeader(200)
		go func() {
			if plt.IsRunning() {
				plt.Stop()
			}
			be.Disconnect()
			os.Exit(0)
		}()
	})

	url := "http://" + addr

	fmt.Printf("EggDuino %s\n", Version)
	if *mockFlag {
		fmt.Println("  Mode:    MOCK (no hardware)")
	} else {
		arduino := findArduino()
		if arduino != "" {
			fmt.Printf("  Arduino: %s\n", arduino)
		} else {
			fmt.Println("  Arduino: not detected (plug in and click Connect)")
		}
	}
	fmt.Printf("  Web UI:  %s\n", url)
	fmt.Println("  Press Ctrl+C to quit")
	fmt.Println()

	go func() {
		time.Sleep(200 * time.Millisecond)
		openBrowser(url)
	}()

	log.Fatal(http.ListenAndServe(addr, nil))
}
