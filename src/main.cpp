/* EggDuino Firmware - EBB Protocol v13 Emulator
 *
 * Originally by Joachim Cerny, 2014
 * Ported to PlatformIO and bugfixed, 2026
 *
 * Implements EBB (EiBotBoard) protocol so Inkscape's Eggbot extension
 * can drive two stepper motors + a pen servo.
 *
 * Target: Arduino Uno + CNC Shield v3 (A4988/DRV8825 drivers)
 *
 * EBB-Command-Reference: http://www.schmalzhaus.com/EBB/EBBCommands.html
 * No homing sequence - power-on position is the reference point.
 * No collision detection.
 *
 * EBB coordinates arrive in 16th-microstep mode. Coordinate transforms
 * use integer math to avoid floating point on AVR.
 */

#include <Arduino.h>
#include <Servo.h>
#include <avr/eeprom.h>
#include "AccelStepper.h"
#include "SerialCommand.h"
#include "button.h"

// ---- Version string returned by 'v' command ----
#define INIT_STRING "EBBv13_and_above Protocol emulated by Eggduino-Firmware V1.7"

// ---- CNC Shield v3 Pin Mapping ----
// Rotation axis = Y on the CNC shield
#define ROT_STEP_PIN  3
#define ROT_DIR_PIN   6

// Pen axis = X on the CNC shield
#define PEN_STEP_PIN  2
#define PEN_DIR_PIN   5

// CNC Shield v3 has a single enable pin for all axes
#define ENABLE_PIN    8

// Servo pin - using the SpinEnable header on CNC Shield v3
// (directly accessible, no conflict with stepper pins)
#define SERVO_PIN     12

// ---- Microstepping config ----
// Hardware is TMC2209 at 32x microstepping (no jumpers = 8, MS1 only = 32).
// EBB protocol sends coordinates in 16th-microstep units.
// Set these to 16 so coordinates pass through 1:1; the web app compensates
// by using 6400 steps/rev (200 * 32) for page width.
#define ROT_MICROSTEP 16
#define PEN_MICROSTEP 16

// ---- Pen axis travel limits (steps from zero) ----
// Set to 0 to disable limits
#define PEN_LIMIT_NEG -300
#define PEN_LIMIT_POS  400

// ---- EEPROM addresses for pen servo positions ----
#define PEN_UP_EE_ADDR   ((uint16_t *)0)
#define PEN_DOWN_EE_ADDR ((uint16_t *)2)

// ---- Optional buttons (uncomment to enable) ----
// #define PRG_BUTTON_PIN        2  // would conflict with ROT_STEP on CNC shield!
// #define PEN_TOGGLE_BUTTON_PIN 12
// #define MOTORS_BUTTON_PIN     4

// ---- Default servo positions (degrees) ----
// Used on first boot when EEPROM is empty (0xFFFF)
#define DEFAULT_PEN_UP_POS    5
#define DEFAULT_PEN_DOWN_POS  20

// ---- Objects ----
AccelStepper rotMotor(AccelStepper::DRIVER, ROT_STEP_PIN, ROT_DIR_PIN);
AccelStepper penMotor(AccelStepper::DRIVER, PEN_STEP_PIN, PEN_DIR_PIN);
Servo penServo;
SerialCommand SCmd;

#ifdef PRG_BUTTON_PIN
  void setprgButtonState();
  Button prgButtonToggle(PRG_BUTTON_PIN, setprgButtonState);
#endif
#ifdef PEN_TOGGLE_BUTTON_PIN
  void doTogglePen();
  Button penToggle(PEN_TOGGLE_BUTTON_PIN, doTogglePen);
#endif
#ifdef MOTORS_BUTTON_PIN
  void toggleMotors();
  Button motorsToggle(MOTORS_BUTTON_PIN, toggleMotors);
#endif

// ---- State variables ----
int penUpPos = DEFAULT_PEN_UP_POS;
int penDownPos = DEFAULT_PEN_DOWN_POS;
int servoRateUp = 0;
int servoRateDown = 0;
long rotStepError = 0;
long penStepError = 0;
int penState = DEFAULT_PEN_UP_POS;
uint32_t nodeCount = 0;
unsigned int layer = 0;
boolean prgButtonState = 0;
boolean motorsEnabled = 0;

const uint8_t rotStepCorrection = 16 / ROT_MICROSTEP;
const uint8_t penStepCorrection = 16 / PEN_MICROSTEP;

// ---- Forward declarations ----
void makeComInterface();
void initHardware();
void loadPenPosFromEE();
void storePenUpPosInEE();
void storePenDownPosInEE();
void sendAck();
void sendError();
void motorsOff();
void motorsOn();
void toggleMotors();
bool parseSMArgs(uint16_t *duration, int *penStepsEBB, int *rotStepsEBB);
void prepareMove(uint16_t duration, int penStepsEBB, int rotStepsEBB);
void moveOneStep();
void moveToDestination();

// EBB command handlers
void sendVersion();
void enableMotors();
void stepperModeConfigure();
void setPen();
void stepperMove();
void togglePen();
void doTogglePen();
void ignore();
void nodeCountIncrement();
void nodeCountDecrement();
void setNodeCount();
void queryNodeCount();
void setLayer();
void queryLayer();
void queryPen();
void queryButton();
void unrecognized(const char *command);
void setprgButtonState();
void queryDebug();
void setHome();
void goHome();

// ==========================================================================
// Setup & Loop
// ==========================================================================

void setup() {
  Serial.begin(115200);
  makeComInterface();
  initHardware();
}

void loop() {
  moveOneStep();
  SCmd.readSerial();

#ifdef PEN_TOGGLE_BUTTON_PIN
  penToggle.check();
#endif
#ifdef MOTORS_BUTTON_PIN
  motorsToggle.check();
#endif
#ifdef PRG_BUTTON_PIN
  prgButtonToggle.check();
#endif
}

// ==========================================================================
// Serial command interface
// ==========================================================================

void makeComInterface() {
  SCmd.addCommand("v",  sendVersion);
  SCmd.addCommand("EM", enableMotors);
  SCmd.addCommand("SC", stepperModeConfigure);
  SCmd.addCommand("SP", setPen);
  SCmd.addCommand("SM", stepperMove);
  SCmd.addCommand("SE", ignore);
  SCmd.addCommand("TP", togglePen);
  SCmd.addCommand("PO", ignore);  // Engraver command - not implemented, fake OK
  SCmd.addCommand("NI", nodeCountIncrement);
  SCmd.addCommand("ND", nodeCountDecrement);
  SCmd.addCommand("SN", setNodeCount);
  SCmd.addCommand("QN", queryNodeCount);
  SCmd.addCommand("SL", setLayer);
  SCmd.addCommand("QL", queryLayer);
  SCmd.addCommand("QP", queryPen);
  SCmd.addCommand("QB", queryButton);
  SCmd.addCommand("QD", queryDebug);
  SCmd.addCommand("HM", setHome);   // Set current position as zero
  SCmd.addCommand("GH", goHome);    // Return to zero position
  SCmd.setDefaultHandler(unrecognized);
}

// ==========================================================================
// EBB Command Handlers
// ==========================================================================

void sendVersion() {
  Serial.print(INIT_STRING);
  Serial.print("\r\n");
}

void queryPen() {
  char state = (penState == penUpPos) ? '1' : '0';
  Serial.print(state);
  Serial.print("\r\n");
  sendAck();
}

void queryButton() {
  Serial.print(String(prgButtonState) + "\r\n");
  sendAck();
  prgButtonState = 0;
}

void queryLayer() {
  Serial.print(String(layer) + "\r\n");
  sendAck();
}

void setLayer() {
  char *arg1 = SCmd.next();
  if (arg1 != NULL) {
    layer = atoi(arg1);
    sendAck();
  } else {
    sendError();
  }
}

void queryNodeCount() {
  Serial.print(String(nodeCount) + "\r\n");
  sendAck();
}

void setNodeCount() {
  char *arg1 = SCmd.next();
  if (arg1 != NULL) {
    nodeCount = atoi(arg1);
    sendAck();
  } else {
    sendError();
  }
}

void nodeCountIncrement() {
  nodeCount++;
  sendAck();
}

void nodeCountDecrement() {
  nodeCount--;
  sendAck();
}

void stepperMove() {
  uint16_t duration = 0;
  int penStepsEBB = 0;
  int rotStepsEBB = 0;

  moveToDestination();

  if (!parseSMArgs(&duration, &penStepsEBB, &rotStepsEBB)) {
    sendError();
    return;
  }

  sendAck();

  if ((penStepsEBB == 0) && (rotStepsEBB == 0)) {
    delay(duration);
    return;
  }

  prepareMove(duration, penStepsEBB, rotStepsEBB);
}

void setPen() {
  char *arg = SCmd.next();
  if (arg == NULL) {
    sendError();
    return;
  }

  int cmd = atoi(arg);
  switch (cmd) {
    case 0:
      penServo.write(penUpPos);
      penState = penUpPos;
      break;
    case 1:
      penServo.write(penDownPos);
      penState = penDownPos;
      break;
    default:
      sendError();
      return;
  }

  char *val = SCmd.next();
  int delayMs = (val != NULL) ? atoi(val) : 500;
  sendAck();
  delay(delayMs);
}

void togglePen() {
  moveToDestination();

  char *arg = SCmd.next();
  int delayMs = (arg != NULL) ? atoi(arg) : 500;

  doTogglePen();
  sendAck();
  delay(delayMs);
}

void doTogglePen() {
  if (penState == penUpPos) {
    penServo.write(penDownPos);
    penState = penDownPos;
  } else {
    penServo.write(penUpPos);
    penState = penUpPos;
  }
}

void enableMotors() {
  char *arg = SCmd.next();
  if (arg == NULL) {
    sendError();
    return;
  }

  int cmd = atoi(arg);
  char *val = SCmd.next();

  // If two args, use the second one (EBB protocol quirk)
  int value = (val != NULL) ? atoi(val) : cmd;

  switch (value) {
    case 0:
      motorsOff();
      sendAck();
      break;
    case 1:
      motorsOn();
      sendAck();
      break;
    default:
      sendError();
      break;
  }
}

void stepperModeConfigure() {
  char *arg = SCmd.next();
  char *val = SCmd.next();
  if (arg == NULL || val == NULL) {
    sendError();
    return;
  }

  int cmd = atoi(arg);
  int value = atoi(val);

  switch (cmd) {
    case 4:
      penDownPos = (int)((float)(value - 6000) / 133.3f);
      storePenDownPosInEE();
      sendAck();
      break;
    case 5:
      penUpPos = (int)((float)(value - 6000) / 133.3f);
      storePenUpPosInEE();
      sendAck();
      break;
    case 6:  // rotMin - ignored
    case 7:  // rotMax - ignored
      sendAck();
      break;
    case 11:
      servoRateUp = value;
      sendAck();
      break;
    case 12:
      servoRateDown = value;
      sendAck();
      break;
    default:
      sendError();
      break;
  }
}

void ignore() {
  sendAck();
}

void unrecognized(const char *command) {
  sendError();
}

// ==========================================================================
// Hardware helpers
// ==========================================================================

void initHardware() {
  loadPenPosFromEE();

  // CNC Shield v3: single enable pin for all axes
  pinMode(ENABLE_PIN, OUTPUT);

  rotMotor.setMaxSpeed(2000.0);
  rotMotor.setAcceleration(10000.0);
  penMotor.setMaxSpeed(2000.0);
  penMotor.setAcceleration(10000.0);
  motorsOff();

  penServo.attach(SERVO_PIN);
  penServo.write(penState);
}

void loadPenPosFromEE() {
  uint16_t upVal = eeprom_read_word(PEN_UP_EE_ADDR);
  uint16_t downVal = eeprom_read_word(PEN_DOWN_EE_ADDR);

  // EEPROM is 0xFFFF when unprogrammed - use defaults in that case
  if (upVal != 0xFFFF && upVal <= 180) {
    penUpPos = upVal;
  }
  if (downVal != 0xFFFF && downVal <= 180) {
    penDownPos = downVal;
  }
  penState = penUpPos;
}

void storePenUpPosInEE() {
  eeprom_update_word(PEN_UP_EE_ADDR, penUpPos);
}

void storePenDownPosInEE() {
  eeprom_update_word(PEN_DOWN_EE_ADDR, penDownPos);
}

void sendAck() {
  Serial.print("OK\r\n");
}

void sendError() {
  Serial.print("unknown CMD\r\n");
}

void motorsOff() {
  digitalWrite(ENABLE_PIN, HIGH);  // CNC Shield: HIGH = disabled
  motorsEnabled = 0;
  Serial.print("DBG motors OFF\r\n");
}

void motorsOn() {
  digitalWrite(ENABLE_PIN, LOW);   // CNC Shield: LOW = enabled
  motorsEnabled = 1;
  Serial.print("DBG motors ON\r\n");
}

void toggleMotors() {
  if (motorsEnabled) {
    motorsOff();
  } else {
    motorsOn();
  }
}

// ==========================================================================
// Motion control
// ==========================================================================

bool parseSMArgs(uint16_t *duration, int *penStepsEBB, int *rotStepsEBB) {
  char *arg1 = SCmd.next();
  if (arg1 == NULL) return false;
  *duration = atoi(arg1);

  char *arg2 = SCmd.next();
  if (arg2 == NULL) return false;
  *penStepsEBB = atoi(arg2);

  char *arg3 = SCmd.next();
  if (arg3 == NULL) return false;
  *rotStepsEBB = atoi(arg3);

  return true;
}

void prepareMove(uint16_t duration, int penStepsEBB, int rotStepsEBB) {
  if (!motorsEnabled) {
    motorsOn();
  }

  // Clamp pen axis to physical limits
  #if PEN_LIMIT_NEG != 0 || PEN_LIMIT_POS != 0
  {
    long targetPos = penMotor.currentPosition() + (long)penStepsEBB;
    if (targetPos < PEN_LIMIT_NEG) {
      penStepsEBB = PEN_LIMIT_NEG - penMotor.currentPosition();
      Serial.print("DBG PEN CLAMPED to neg limit\r\n");
    } else if (targetPos > PEN_LIMIT_POS) {
      penStepsEBB = PEN_LIMIT_POS - penMotor.currentPosition();
      Serial.print("DBG PEN CLAMPED to pos limit\r\n");
    }
  }
  #endif

  Serial.print("DBG SM dur=");
  Serial.print(duration);
  Serial.print(" pen=");
  Serial.print(penStepsEBB);
  Serial.print(" rot=");
  Serial.print(rotStepsEBB);
  Serial.print(" penPos=");
  Serial.print(penMotor.currentPosition());
  Serial.print("\r\n");

  if ((rotStepCorrection == 1) && (penStepCorrection == 1)) {
    // Coordinate systems are identical (16x microstepping)
    rotMotor.move(rotStepsEBB);
    rotMotor.setSpeed(abs((float)rotStepsEBB * 1000.0f / (float)duration));
    penMotor.move(penStepsEBB);
    penMotor.setSpeed(abs((float)penStepsEBB * 1000.0f / (float)duration));
  } else {
    // Scale EBB coordinates to our microstepping setting using integer math
    // Multiply by 16 to maintain precision, divide at the end
    long rotSteps = ((long)rotStepsEBB * 16 / rotStepCorrection) + rotStepError;
    long penSteps = ((long)penStepsEBB * 16 / penStepCorrection) + penStepError;

    int rotStepsToGo = (int)(rotSteps / 16);
    int penStepsToGo = (int)(penSteps / 16);

    rotStepError = rotSteps - ((long)rotStepsToGo * 16);
    penStepError = penSteps - ((long)penStepsToGo * 16);

    float rotSpeed = (float)abs((long)rotStepsToGo * 1000L / (long)duration);
    float penSpeed = (float)abs((long)penStepsToGo * 1000L / (long)duration);

    rotMotor.move(rotStepsToGo);
    rotMotor.setSpeed(rotSpeed);
    penMotor.move(penStepsToGo);
    penMotor.setSpeed(penSpeed);
  }
}

void moveOneStep() {
  if (penMotor.distanceToGo() || rotMotor.distanceToGo()) {
    penMotor.runSpeedToPosition();
    rotMotor.runSpeedToPosition();
  }
}

void moveToDestination() {
  while (penMotor.distanceToGo() || rotMotor.distanceToGo()) {
    penMotor.runSpeedToPosition();
    rotMotor.runSpeedToPosition();
  }
}

void setprgButtonState() {
  prgButtonState = 1;
}

void setHome() {
  moveToDestination();
  rotMotor.setCurrentPosition(0);
  penMotor.setCurrentPosition(0);
  sendAck();
}

void goHome() {
  if (!motorsEnabled) motorsOn();
  // Move back to 0,0 at a moderate speed
  rotMotor.moveTo(0);
  rotMotor.setSpeed(abs(rotMotor.distanceToGo()) > 0 ? 400.0 : 0);
  penMotor.moveTo(0);
  penMotor.setSpeed(abs(penMotor.distanceToGo()) > 0 ? 400.0 : 0);
  moveToDestination();
  sendAck();
}

void queryDebug() {
  Serial.print("DBG motors=");
  Serial.print(motorsEnabled ? "ON" : "OFF");
  Serial.print(" enablePin=");
  Serial.print(digitalRead(ENABLE_PIN) == LOW ? "LOW(on)" : "HIGH(off)");
  Serial.print(" penUp=");
  Serial.print(penUpPos);
  Serial.print(" penDown=");
  Serial.print(penDownPos);
  Serial.print(" penState=");
  Serial.print(penState);
  Serial.print(" rotTarget=");
  Serial.print(rotMotor.distanceToGo());
  Serial.print(" penTarget=");
  Serial.print(penMotor.distanceToGo());
  Serial.print(" rotPos=");
  Serial.print(rotMotor.currentPosition());
  Serial.print(" penPos=");
  Serial.print(penMotor.currentPosition());
  Serial.print("\r\n");
  sendAck();
}
