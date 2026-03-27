# EggDuino Modern

A modernized version of [EggDuino](https://github.com/cocktailyogi/EggDuino), the Arduino firmware for DIY [EggBot](https://egg-bot.com/)/spherebot drawing machines. This project updates the original to work with the widely available CNC Shield v3 (instead of hand-wired stepper drivers) and replaces the Inkscape plugin workflow with a standalone browser-based control interface -- no Inkscape installation or plugin patching required.

## Highlights

- Single binary (`eggduino`) serves a web UI and handles serial communication -- no browser plugins, no Python, no Inkscape
- Load any SVG, configure speeds and pen positions, and plot directly from your browser
- Full SVG path support including cubic/quadratic Beziers, arcs, and transforms
- Live preview with zoom, pan, position tracking dot, and configurable background
- Firmware implements EBB Protocol v13 for compatibility with existing eggbot tooling
- Pen axis safety limits prevent mechanical collisions
- Works on Linux, macOS, and Windows

## How It Works

The project has two parts:

**Firmware** (PlatformIO/Arduino) runs on an Arduino Uno with a CNC Shield v3 and stepper drivers. It implements the [EBB protocol](http://www.schmalzhaus.com/EBB/EBBCommands.html) over serial, driving two stepper motors (egg rotation + pen position) and a servo (pen lift).

**Desktop app** (Go) is a single binary that embeds the web UI. It auto-detects the Arduino, opens a serial connection, and serves a browser-based control panel on localhost. The web UI parses SVGs client-side, generates motion commands, and sends them to the firmware through a WebSocket bridge.

## Usage

Flash the firmware to your Arduino Uno:

```
pio run -t upload
```

Run the desktop app:

```
./eggduino
```

A browser window opens automatically. From there:

1. Click **Connect** to attach to the Arduino
2. Use the **Manual** tab to jog motors and set your zero position
3. Load an SVG file (drag and drop or file picker)
4. Adjust speeds, pen positions, and smoothness in **Settings**
5. Click **Plot**

## Installation

### Firmware

Requires [PlatformIO](https://platformio.org/install/cli). From the project root:

```
pio run -t upload
```

The firmware targets Arduino Uno at 115200 baud. Pin mapping assumes a CNC Shield v3 with DRV8825 or A4988 stepper drivers.

### Desktop App

Download a pre-built binary for your platform from the releases page, or build from source with Go 1.21+:

```
go build -ldflags="-s -w" -o eggduino ./cmd/eggduino/
```

Cross-compile for other platforms:

```
GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o eggduino-macos   ./cmd/eggduino/
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o eggduino.exe     ./cmd/eggduino/
GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o eggduino-linux   ./cmd/eggduino/
```

## Preparing SVGs

EggDuino accepts standard SVG files. The SVG is automatically scaled to fit the machine's drawing area, so any dimensions work, but keep the following in mind:

- **Vector paths only.** Raster images, embedded bitmaps, and text elements are ignored. Convert text to paths in your editor before exporting (in Inkscape: Path > Object to Path).
- **Recommended canvas size:** 6400 x 1400 pixels matches the default machine dimensions (6400 steps for one full egg rotation at 32x microstepping, 1400 steps of pen travel). SVGs with other aspect ratios are scaled uniformly to fit.
- **Supported elements:** `<path>`, `<rect>`, `<circle>`, `<ellipse>`, `<line>`, `<polyline>`, `<polygon>`. Groups (`<g>`) and transforms are handled correctly.
- **Stroke, not fill.** The plotter traces paths -- it does not fill shapes. Design your artwork as outlines/strokes. If your SVG uses filled shapes, the plotter will trace their outlines.
- **Simplify where possible.** Fewer and simpler paths plot faster. Use your editor's "Simplify" or "Reduce nodes" feature to eliminate unnecessary detail, especially on curves that were converted from raster traces.
- **Smoothness setting.** The web UI has a smoothness slider (0.1-5.0) that controls how finely curves are subdivided into line segments. Lower values produce smoother curves but more segments (slower plotting). The default of 0.5 works well for most artwork.

Any vector editor that exports SVG works: Inkscape, Illustrator, Figma, Affinity Designer, or even hand-written SVG.

## Hardware

For the frame, you can 3D print one of these designs (both include full hardware/BOM lists):

- [Sphere-O-Bot](https://www.thingiverse.com/thing:3512980) -- the original design
- [OKMI EggBot Remix](https://www.printables.com/model/203407-okmi-eggbot-remix) -- a cleaner remix with easier assembly

Electronics:

- Arduino Uno (or compatible)
- CNC Shield v3
- 2x stepper drivers (DRV8825 or A4988)
- 2x NEMA 17 stepper motors
- 1x hobby servo (pen lift)
- 12V power supply for the CNC shield

### Pin Mapping

| Function | CNC Shield | Arduino Pin |
|----------|-----------|-------------|
| Pen step | X axis | 2 |
| Pen dir | X axis | 5 |
| Rotation step | Y axis | 3 |
| Rotation dir | Y axis | 6 |
| Motor enable | All | 8 |
| Servo signal | End-stop header | 12 |

### Firmware Configuration

Edit the defines at the top of `src/main.cpp` to match your machine:

- `PEN_LIMIT_NEG` / `PEN_LIMIT_POS` -- pen axis travel limits in steps from zero
- `ROT_MICROSTEP` / `PEN_MICROSTEP` -- microstepping mode (must match jumper settings)
- `DEFAULT_PEN_UP_POS` / `DEFAULT_PEN_DOWN_POS` -- servo positions in degrees

## Credits

This project builds on the work of several others:

- [EggBot](https://egg-bot.com/) by Evil Mad Scientist Laboratories -- the original egg-drawing robot and the [EBB protocol](http://www.schmalzhaus.com/EBB/EBBCommands.html) this firmware emulates
- [EggDuino](https://github.com/cocktailyogi/EggDuino) by Joachim Cerny -- the Arduino EBB implementation this firmware was ported from
- [AccelStepper](http://www.airspayce.com/mikem/arduino/AccelStepper/) by Mike McCauley -- stepper motor library
- [SerialCommand](https://github.com/kroimon/Arduino-SerialCommand) by Stefan Rado -- serial command parsing library

## License

The original EggDuino firmware and bundled libraries are licensed under their respective terms (GPL v2+ for EggDuino, LGPL v3+ for SerialCommand). See individual source files for details.
