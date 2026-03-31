# EggDuino Modern

A modernized version of [EggDuino](https://github.com/cocktailyogi/EggDuino), the Arduino firmware for DIY [EggBot](https://egg-bot.com/)/spherebot drawing machines. This version targets the CNC Shield v3 with TMC2209 stepper drivers and includes a standalone browser-based control interface that replaces the Inkscape plugin workflow.

## Overview

The project has two parts:

**Firmware** (PlatformIO/Arduino) runs on an Arduino Uno with a CNC Shield v3 and TMC2209 drivers. It implements the [EBB protocol](http://www.schmalzhaus.com/EBB/EBBCommands.html) over serial, driving two stepper motors (egg rotation + pen position) and a servo (pen lift).

**Desktop app** (Go) is a single binary that embeds the web UI. It auto-detects the Arduino, opens a serial connection, and serves a browser-based control panel on localhost. The web UI parses SVGs client-side, generates motion commands, and sends them to the firmware through a WebSocket bridge.

Features:

- Full SVG path support including cubic/quadratic Beziers, arcs, and transforms
- Live preview with zoom, pan, and position tracking
- Pen axis safety limits to prevent mechanical collisions
- EBB Protocol v13 compatible
- Runs on Linux, macOS, and Windows

## Hardware

### Frame

3D-printable frame designs (both include hardware/BOM lists):

- [Sphere-O-Bot](https://www.thingiverse.com/thing:3512980)
- [OKMI EggBot Remix](https://www.printables.com/model/203407-okmi-eggbot-remix)

### Electronics

- Arduino Uno (or compatible)
- CNC Shield v3
- 2x TMC2209 stepper drivers
- 2x NEMA 17 stepper motors
- 1x hobby servo (pen lift)
- 12V power supply for the CNC shield

### TMC2209 Configuration

The TMC2209 drivers are configured for 32x microstepping. On the CNC Shield v3, set only the MS1 jumper (no jumpers = 8x, MS1 only = 32x). The firmware accounts for this internally -- coordinates use EBB-standard 16th-microstep units, and the web app uses 6400 steps/rev (200 full steps * 32 microsteps) for the page width.

### Pin Mapping

| Function | CNC Shield | Arduino Pin |
|----------|-----------|-------------|
| Pen step | X axis | 2 |
| Pen dir | X axis | 5 |
| Rotation step | Y axis | 3 |
| Rotation dir | Y axis | 6 |
| Motor enable | All | 8 |
| Servo signal | End-stop header | 12 |

## Installation

### Firmware

Requires [PlatformIO](https://platformio.org/install/cli). From the project root:

```
pio run -t upload
```

The firmware targets Arduino Uno at 115200 baud.

### Desktop App

Download a pre-built binary from the releases page, or build from source with Go 1.21+:

```
go build -ldflags="-s -w" -o eggduino ./cmd/eggduino/
```

Cross-compile for other platforms:

```
GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o eggduino-macos   ./cmd/eggduino/
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o eggduino.exe     ./cmd/eggduino/
GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o eggduino-linux   ./cmd/eggduino/
```

## Usage

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

## Calibration

There is no automatic homing -- the machine uses its power-on position as the reference point. Calibration involves setting the pen servo positions, the pen axis travel limits, and the zero point.

### Pen Servo (Up/Down Positions)

The servo angles for pen-up and pen-down are stored in EEPROM and persist across power cycles. Defaults are 5 degrees (up) and 20 degrees (down).

To adjust, use the pen position sliders in the web UI's **Settings** tab, then click **Send pen positions to board**. Test with the pen-up/pen-down buttons until the servo lifts and lowers the pen cleanly without excessive pressure or too much clearance.

### Pen Axis Limits

The pen axis has software travel limits that prevent the pen arm from crashing into the frame. These are stored in EEPROM as step counts relative to the zero position. Defaults are -400 (negative limit) and +200 (positive limit).

To set limits using the web UI:

1. In the **Manual** tab, check **Ignore pen limits when jogging**
2. Jog the pen axis to one mechanical extreme (where it's about to hit the frame)
3. Click **Set neg** or **Set pos** to capture that position as a limit
4. Jog to the opposite extreme and capture the other limit
5. Uncheck **Ignore pen limits when jogging**

The limits are saved to EEPROM immediately and persist across power cycles. The firmware validates stored limits on startup -- if they're corrupted or out of range, defaults are restored.

### Setting Zero

Position the egg and pen where you want the origin to be, then click **Set Home** in the Manual tab. This resets both axes to position 0,0. Use **Go Home** to return to this position after jogging or plotting.

### Firmware Configuration

For configuration that isn't exposed in the web UI, edit the defines at the top of `src/main.cpp`:

- `DEFAULT_PEN_LIMIT_NEG` / `DEFAULT_PEN_LIMIT_POS` -- default pen axis travel limits (used when EEPROM is uninitialized)
- `ROT_MICROSTEP` / `PEN_MICROSTEP` -- microstepping divisor (should match your driver jumper settings)
- `DEFAULT_PEN_UP_POS` / `DEFAULT_PEN_DOWN_POS` -- default servo positions in degrees

## Preparing SVGs

The plotter accepts standard SVG files. The SVG is automatically scaled to fit the machine's drawing area.

- **Vector paths only.** Raster images, embedded bitmaps, and text elements are ignored. Convert text to paths before exporting (in Inkscape: Path > Object to Path).
- **Recommended canvas size:** 3200 x 700 pixels matches the default machine dimensions (3200 steps for one full egg rotation, 700 steps of pen travel). Other aspect ratios are scaled uniformly to fit.
- **Supported elements:** `<path>`, `<rect>`, `<circle>`, `<ellipse>`, `<line>`, `<polyline>`, `<polygon>`. Groups (`<g>`) and transforms are handled.
- **Stroke, not fill.** The plotter traces paths, it does not fill shapes. Design artwork as outlines. Filled shapes will have their outlines traced.
- **Simplify where possible.** Fewer paths plot faster. Use your editor's simplify/reduce nodes feature on curves converted from raster traces.
- **Smoothness setting.** The web UI has a smoothness slider (0.1-5.0) that controls curve subdivision. Lower values = smoother curves but more segments. Default 0.5 works well for most artwork.

## Credits

- [EggBot](https://egg-bot.com/) by Evil Mad Scientist Laboratories -- the original egg-drawing robot and the [EBB protocol](http://www.schmalzhaus.com/EBB/EBBCommands.html) this firmware emulates
- [EggDuino](https://github.com/cocktailyogi/EggDuino) by Joachim Cerny -- the Arduino EBB implementation this firmware was ported from
- [AccelStepper](http://www.airspayce.com/mikem/arduino/AccelStepper/) by Mike McCauley -- stepper motor library
- [SerialCommand](https://github.com/kroimon/Arduino-SerialCommand) by Stefan Rado -- serial command parsing library

## License

The original EggDuino firmware and bundled libraries are licensed under their respective terms (GPL v2+ for EggDuino, LGPL v3+ for SerialCommand). See individual source files for details.
