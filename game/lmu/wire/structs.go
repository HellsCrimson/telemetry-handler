// Package wire defines the binary wire format the lmu-bridge sidecar uses to
// stream Le Mans Ultimate / rFactor 2 telemetry to the main app, plus the
// chunking/reassembly envelope that lets a frame span several UDP datagrams.
//
// It is imported by BOTH the sidecar (which fills these structs from the rF2
// shared memory and marshals them) and the main app's lmu package (which
// unmarshals them). Sharing one definition is what keeps the two ends in sync:
// there are no hand-maintained byte offsets on the wire, only Go structs run
// through encoding/binary.
//
// Layout note: the per-vehicle structs (VehicleTelemetry, Wheel,
// VehicleScoring, ScoringInfo) deliberately mirror the rF2 Shared Memory Map
// Plugin's C structs field-for-field (TheIronWolf's rF2State.h, #pragma
// pack(4)). The rF2 structs contain NO implicit padding — every alignment gap
// is filled by an explicit mExpansion/mUnused array — so a tightly packed Go
// struct (which is exactly what encoding/binary produces) reads them
// byte-for-byte. The blank `_ [N]byte` fields below reproduce those expansion
// arrays so the struct size matches the rF2 stride; structs_test.go asserts the
// sizes (1888/260/584/548) so a layout mistake fails the build.
package wire

// Vec3 mirrors rF2Vec3 (three doubles, 24 bytes).
type Vec3 struct{ X, Y, Z float64 }

// Wheel mirrors rF2Wheel (TelemWheelV01), sizeof == 260 under pack(4).
type Wheel struct {
	SuspensionDeflection  float64 // meters
	RideHeight            float64 // meters
	SuspForce             float64 // pushrod load, Newtons
	BrakeTemp             float64 // Celsius
	BrakePressure         float64 // 0.0-1.0
	Rotation              float64 // radians/sec
	LateralPatchVel       float64
	LongitudinalPatchVel  float64
	LateralGroundVel      float64
	LongitudinalGroundVel float64
	Camber                float64 // radians
	LateralForce          float64 // Newtons
	LongitudinalForce     float64 // Newtons
	TireLoad              float64 // Newtons
	GripFract             float64 // fraction of contact patch sliding
	Pressure              float64 // kPa

	Temperature [3]float64 // Kelvin, left/center/right
	Wear        float64    // 0.0-1.0

	TerrainName             [16]byte // TDF material prefix
	SurfaceType             uint8    // 0=dry,1=wet,2=grass,3=dirt,4=gravel,5=kerb,6=special
	Flat                    uint8    // bool: tire flat
	Detached                uint8    // bool: wheel detached
	StaticUndeflectedRadius uint8    // centimeters

	VerticalTireDeflection float64
	WheelYLocation         float64
	Toe                    float64

	TireCarcassTemperature    float64    // Kelvin
	TireInnerLayerTemperature [3]float64 // Kelvin

	_ [24]byte // rF2Wheel.mExpansion
}

// VehicleTelemetry mirrors rF2VehicleTelemetry (TelemInfoV01), sizeof == 1888.
type VehicleTelemetry struct {
	ID          int32 // slot ID (matches VehicleScoring.ID)
	DeltaTime   float64
	ElapsedTime float64
	LapNumber   int32
	LapStartET  float64
	VehicleName [64]byte
	TrackName   [64]byte

	Pos           Vec3
	LocalVel      Vec3
	LocalAccel    Vec3
	Ori           [3]Vec3 // orientation matrix rows
	LocalRot      Vec3
	LocalRotAccel Vec3

	Gear            int32 // -1=reverse, 0=neutral, 1+=forward
	EngineRPM       float64
	EngineWaterTemp float64 // Celsius
	EngineOilTemp   float64 // Celsius
	ClutchRPM       float64

	UnfilteredThrottle float64 // 0..1
	UnfilteredBrake    float64 // 0..1
	UnfilteredSteering float64 // -1..1
	UnfilteredClutch   float64 // 0..1

	FilteredThrottle float64
	FilteredBrake    float64
	FilteredSteering float64
	FilteredClutch   float64

	SteeringShaftTorque float64
	Front3rdDeflection  float64
	Rear3rdDeflection   float64

	FrontWingHeight float64
	FrontRideHeight float64
	RearRideHeight  float64
	Drag            float64
	FrontDownforce  float64
	RearDownforce   float64

	Fuel           float64 // liters
	EngineMaxRPM   float64
	ScheduledStops uint8
	Overheating    uint8 // bool
	Detached       uint8 // bool
	Headlights     uint8 // bool
	DentSeverity   [8]uint8

	LastImpactET        float64
	LastImpactMagnitude float64
	LastImpactPos       Vec3

	EngineTorque  float64
	CurrentSector int32 // pitlane stored in sign bit

	SpeedLimiter           uint8
	MaxGears               uint8
	FrontTireCompoundIndex uint8
	RearTireCompoundIndex  uint8
	FuelCapacity           float64
	FrontFlapActivated     uint8
	RearFlapActivated      uint8
	RearFlapLegalStatus    uint8
	IgnitionStarter        uint8

	FrontTireCompoundName [18]byte
	RearTireCompoundName  [18]byte

	SpeedLimiterAvailable uint8
	AntiStallActivated    uint8
	_                     [2]byte // mUnused[2]

	VisualSteeringWheelRange float32

	RearBrakeBias              float64
	TurboBoostPressure         float64
	PhysicsToGraphicsOffset    [3]float32
	PhysicalSteeringWheelRange float32 // lock-to-lock degrees

	BatteryChargeFraction float64 // 0.0-1.0

	ElectricBoostMotorTorque      float64
	ElectricBoostMotorRPM         float64
	ElectricBoostMotorTemperature float64
	ElectricBoostWaterTemperature float64
	ElectricBoostMotorState       uint8 // 0=unavail,1=inactive,2=propulsion,3=regen

	_ [111]byte // mExpansion

	Wheels [4]Wheel // front-left, front-right, rear-left, rear-right
}

// VehicleScoring mirrors rF2VehicleScoring (VehicleScoringInfoV01), sizeof == 584.
type VehicleScoring struct {
	ID           int32
	DriverName   [32]byte
	VehicleName  [64]byte
	TotalLaps    int16
	Sector       int8 // 0=sector3,1=sector1,2=sector2
	FinishStatus int8 // 0=none,1=finished,2=dnf,3=dq
	LapDist      float64
	PathLateral  float64
	TrackEdge    float64

	BestSector1 float64
	BestSector2 float64
	BestLapTime float64
	LastSector1 float64
	LastSector2 float64
	LastLapTime float64
	CurSector1  float64
	CurSector2  float64

	NumPitstops  int16
	NumPenalties int16
	IsPlayer     uint8 // bool

	Control      uint8 // who's in control: 0=local player,1=AI,2=remote (signed char, all values >=0 here)
	InPits       uint8 // bool
	Place        uint8 // 1-based position
	VehicleClass [32]byte

	TimeBehindNext   float64
	LapsBehindNext   int32
	TimeBehindLeader float64
	LapsBehindLeader int32
	LapStartET       float64

	Pos           Vec3
	LocalVel      Vec3
	LocalAccel    Vec3
	Ori           [3]Vec3
	LocalRot      Vec3
	LocalRotAccel Vec3

	Headlights      uint8
	PitState        uint8 // 0=none,1=request,2=entering,3=stopped,4=exiting
	ServerScored    uint8
	IndividualPhase uint8

	Qualification int32

	TimeIntoLap      float64
	EstimatedLapTime float64

	PitGroup      [24]byte
	Flag          uint8 // 0=green,6=blue
	UnderYellow   uint8 // bool
	CountLapFlag  uint8
	InGarageStall uint8 // bool

	UpgradePack [16]uint8

	PitLapDist     float32
	BestLapSector1 float32
	BestLapSector2 float32

	_ [48]byte // mExpansion
}

// ScoringInfo mirrors rF2ScoringInfo (ScoringInfoV01), sizeof == 548. It carries
// the session globals and the base weather (the dedicated Weather buffer adds
// finer detail). Note the embedded pointer fields are 8 bytes (64-bit plugin).
type ScoringInfo struct {
	TrackName [64]byte
	Session   int32 // 0=testday 1-4=practice 5-8=qual 9=warmup 10-13=race
	CurrentET float64
	EndET     float64
	MaxLaps   int32
	LapDist   float64
	_         [8]byte // pointer1 (results stream, 64-bit)

	NumVehicles int32

	GamePhase       uint8
	YellowFlagState int8
	SectorFlag      [3]int8
	StartLight      uint8
	NumRedLights    uint8
	InRealtime      uint8 // bool
	PlayerName      [32]byte
	PlrFileName     [64]byte

	// weather
	DarkCloud      float64 // 0.0-1.0
	Raining        float64 // 0.0-1.0
	AmbientTemp    float64 // Celsius
	TrackTemp      float64 // Celsius
	Wind           Vec3
	MinPathWetness float64
	MaxPathWetness float64

	GameMode            uint8
	IsPasswordProtected uint8 // bool
	ServerPort          uint16
	ServerPublicIP      uint32
	MaxPlayers          int32
	ServerName          [32]byte
	StartET             float32

	AvgPathWetness float64

	_ [200]byte // mExpansion
	_ [8]byte   // pointer2 (vehicle array, 64-bit)
}
