package main

import (
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
)

// extractField pulls the value after a "key=" prefix from a space-separated string.
func extractField(s, prefix string) string {
	idx := strings.Index(s, prefix)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(prefix):]
	end := strings.IndexByte(rest, ' ')
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// PlotSettings matches what the browser sends.
type PlotSettings struct {
	PenUpDeg     float64 `json:"penUpDeg"`
	PenDownDeg   float64 `json:"penDownDeg"`
	DrawSpeed    float64 `json:"drawSpeed"`
	TravelSpeed  float64 `json:"travelSpeed"`
	PenUpDelay   int     `json:"penUpDelay"`
	PenDownDelay int     `json:"penDownDelay"`
	ReversePen   bool    `json:"reversePen"`
	ReverseRot   bool    `json:"reverseRot"`
}

// PlotProgress is streamed back to the browser.
type PlotProgress struct {
	Progress float64 `json:"progress"` // 0-100
	PosX     float64 `json:"posX"`     // canvas X
	PosY     float64 `json:"posY"`     // canvas Y
	Done     bool    `json:"done"`
	Error    string  `json:"error,omitempty"`
}

// Plotter runs a plot job on a Backend.
type Plotter struct {
	mu       sync.Mutex
	running  bool
	paused   bool
	stopped  bool
	progress chan PlotProgress
}

func NewPlotter() *Plotter {
	return &Plotter{}
}

func (p *Plotter) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *Plotter) Pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = true
}

func (p *Plotter) Resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paused = false
}

func (p *Plotter) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	p.paused = false
}

func (p *Plotter) isPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

func (p *Plotter) isStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

// Run executes a plot. Blocks until complete. Sends progress to the channel.
// paths are in canvas coordinates (3200x800 standard eggbot canvas).
func (p *Plotter) Run(be Backend, paths []Polyline, settings PlotSettings,
	canvasW, canvasH float64, penLimNeg, penLimPos int) <-chan PlotProgress {

	p.mu.Lock()
	p.running = true
	p.paused = false
	p.stopped = false
	p.progress = make(chan PlotProgress, 100)
	p.mu.Unlock()

	ch := p.progress

	go func() {
		defer func() {
			p.mu.Lock()
			p.running = false
			p.mu.Unlock()
			close(ch)
		}()

		err := p.runPlot(be, paths, settings, canvasW, canvasH, penLimNeg, penLimPos, ch)
		if err != nil {
			ch <- PlotProgress{Error: err.Error(), Done: true}
		} else {
			ch <- PlotProgress{Progress: 100, Done: true}
		}
	}()

	return ch
}

func (p *Plotter) runPlot(be Backend, paths []Polyline, s PlotSettings,
	canvasW, canvasH float64, penLimNeg, penLimPos int, ch chan PlotProgress) error {

	// Coordinate scaling
	hwRotRange := 3200.0
	hwPenRange := float64(penLimPos - penLimNeg)
	rotScale := hwRotRange / canvasW
	penScale := hwPenRange / canvasH

	revP := 1.0
	if s.ReversePen {
		revP = -1.0
	}
	revR := 1.0
	if s.ReverseRot {
		revR = -1.0
	}

	// Center Y: canvas 0→canvasH becomes -canvasH/2→+canvasH/2
	yOffset := canvasH / 2.0
	for i := range paths {
		for j := range paths[i] {
			paths[i][j].Y -= yOffset
		}
	}

	// Count segments for progress
	totalSegs := 0
	for _, path := range paths {
		totalSegs += len(path) // segments + 1 travel per path
	}
	totalSegs++ // return home
	doneSeg := 0

	curX, curY := 0.0, 0.0
	penIsUp := true

	// Helper: send serial command
	send := func(cmd string) error {
		_, err := be.Send(cmd)
		return err
	}

	// Helper: move to canvas coordinate
	moveTo := func(x, y, speed float64) error {
		dx := x - curX
		dy := y - curY
		canvasDist := math.Hypot(dx, dy)
		if canvasDist < 0.5 {
			curX, curY = x, y
			return nil
		}

		rotSteps := math.Round(dx * rotScale * revR)
		penSteps := math.Round(dy * penScale * revP)
		stepDist := math.Hypot(rotSteps, penSteps)
		dur := int(math.Max(1, math.Ceil(1000*stepDist/speed)))

		err := send(fmt.Sprintf("SM,%d,%d,%d", dur, int(penSteps), int(rotSteps)))
		curX, curY = x, y
		return err
	}

	penUp := func() error {
		if !penIsUp {
			err := send(fmt.Sprintf("SP,0,%d", s.PenUpDelay))
			penIsUp = true
			return err
		}
		return nil
	}

	penDown := func() error {
		if penIsUp {
			err := send(fmt.Sprintf("SP,1,%d", s.PenDownDelay))
			penIsUp = false
			return err
		}
		return nil
	}

	emitProgress := func() {
		pct := 0.0
		if totalSegs > 0 {
			pct = float64(doneSeg) / float64(totalSegs) * 100
		}
		select {
		case ch <- PlotProgress{Progress: pct, PosX: curX, PosY: curY}:
		default: // don't block if channel is full
		}
	}

	// Configure servo — only send SC if values differ from firmware,
	// because EEPROM writes glitch the servo PWM signal.
	upVal := int(math.Round(s.PenUpDeg*133.3 + 6000))
	downVal := int(math.Round(s.PenDownDeg*133.3 + 6000))
	needServoConfig := true
	qdLines, qdErr := be.Send("QD")
	if qdErr == nil {
		for _, l := range qdLines {
			if strings.Contains(l, "penUp=") {
				// Parse current values: "... penUp=5 penDown=20 ..."
				currentUp := -1
				currentDown := -1
				fmt.Sscanf(extractField(l, "penUp="), "%d", &currentUp)
				fmt.Sscanf(extractField(l, "penDown="), "%d", &currentDown)
				expectedUp := int((float64(upVal) - 6000) / 133.3)
				expectedDown := int((float64(downVal) - 6000) / 133.3)
				if currentUp == expectedUp && currentDown == expectedDown {
					needServoConfig = false
					log.Printf("[plot] Servo config unchanged (up=%d° down=%d°), skipping SC", currentUp, currentDown)
				}
			}
		}
	}
	if needServoConfig {
		if err := send(fmt.Sprintf("SC,5,%d", upVal)); err != nil {
			return err
		}
		if err := send(fmt.Sprintf("SC,4,%d", downVal)); err != nil {
			return err
		}
	}

	// Enable motors, force pen up (don't trust tracked state at plot start)
	if err := send("EM,1"); err != nil {
		return err
	}
	if err := send(fmt.Sprintf("SP,0,%d", s.PenUpDelay)); err != nil {
		return err
	}
	penIsUp = true

	log.Printf("[plot] Starting: %d paths, canvasW=%.0f canvasH=%.0f penScale=%.3f rotScale=%.3f",
		len(paths), canvasW, canvasH, penScale, rotScale)
	log.Printf("[plot] Settings: penUp=%.1f° penDown=%.1f° drawSpeed=%.0f travelSpeed=%.0f revPen=%v revRot=%v",
		s.PenUpDeg, s.PenDownDeg, s.DrawSpeed, s.TravelSpeed, s.ReversePen, s.ReverseRot)

	// Plot each path
	for pathIdx, polyline := range paths {
		if p.isStopped() {
			log.Printf("[plot] Stopped at path %d/%d", pathIdx, len(paths))
			break
		}
		for p.isPaused() {
			if p.isStopped() {
				break
			}
		}

		if len(polyline) < 2 {
			continue
		}

		// Travel to start of this path.
		// If the travel distance is short, skip the pen lift — the connecting
		// line is invisible on a real egg and avoids 600ms of servo delay.
		const minLiftDist = 50.0 // canvas units — below this, keep pen down
		start := polyline[0]
		travelDist := math.Hypot(start.X-curX, start.Y-curY)
		if travelDist > minLiftDist {
			if err := penUp(); err != nil {
				return err
			}
			if err := moveTo(start.X, start.Y, s.TravelSpeed); err != nil {
				return err
			}
		} else if travelDist > 0.5 {
			// Short hop — travel with pen down at draw speed
			if err := moveTo(start.X, start.Y, s.DrawSpeed); err != nil {
				return err
			}
		}
		doneSeg++
		emitProgress()

		// Draw the path
		if err := penDown(); err != nil {
			return err
		}
		for i := 1; i < len(polyline); i++ {
			if p.isStopped() {
				break
			}
			if err := moveTo(polyline[i].X, polyline[i].Y, s.DrawSpeed); err != nil {
				return err
			}
			doneSeg++
			emitProgress()
		}
	}

	// Return home
	if err := penUp(); err != nil {
		return err
	}
	if _, err := be.Send("GH"); err != nil {
		return err
	}
	doneSeg++
	emitProgress()

	// Disable motors
	send("EM,0")

	log.Printf("[plot] Complete: %d moves, final pos canvas=(%.1f, %.1f)", doneSeg, curX, curY)
	return nil
}
