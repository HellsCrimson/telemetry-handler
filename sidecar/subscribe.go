package main

// Buffer subscription. The plugin leaves Graphics (bit 32) and Weather (bit
// 128) UNSUBSCRIBED by default (see TheIronWolf's README), so those buffers
// don't update until subscribed. There are two ways to fix that:
//
//  1. Statically: set "UnsubscribedBuffersMask": 0 in the game's
//     UserData/player/CustomPluginVariables.JSON (documented in README.md).
//  2. Dynamically: write the PluginControl input buffer's
//     mRequestEnableBuffersMask, which the plugin honours at runtime IF
//     mPluginControlInputEnabled is set. subscribeAll() does this best-effort.
//
// The sidecar also reads Extended.mUnsubscribedBuffersMask each tick and warns
// (throttled) while Graphics/Weather remain unsubscribed.

const pluginControlMapName = `$rFactor2SMMP_PluginControl$`

// Subscription bit values for UnsubscribedBuffersMask / mRequestEnableBuffersMask.
const (
	bufTelemetry     = 1
	bufScoring       = 2
	bufRules         = 4
	bufMultiRules    = 8
	bufForceFeedback = 16
	bufGraphics      = 32
	bufPitInfo       = 64
	bufWeather       = 128
	bufAll           = 255
)

// PluginControl buffer layout (8-byte version block prefix, then the ISI body):
//
//	@0  mVersionUpdateBegin (u32)
//	@4  mVersionUpdateEnd   (u32)
//	@8  mLayoutVersion      (long)  -- rF2MappedInputBufferHeader
//	@12 mRequestEnableBuffersMask (long)
//	@16 mRequestHWControlInput     (bool)
const (
	pcOffVersionBegin = 0
	pcOffVersionEnd   = 4
	pcOffLayout       = prefVersionBlock     // 8
	pcOffEnableMask   = prefVersionBlock + 4 // 12

	// SUPPORTED_LAYOUT_VERSION for rF2PluginControl in rF2State.h.
	pluginControlLayoutVersion = 1
)

// subscribeAll requests that the plugin enable every (requestable) buffer via
// the PluginControl input buffer, using the version-block handshake the plugin
// expects (bump begin, write the request, bump end to commit).
func subscribeAll(m *mapping) {
	begin := m.readUint32(pcOffVersionBegin)
	end := m.readUint32(pcOffVersionEnd)
	next := end + 1
	if begin > end {
		next = begin + 1
	}
	m.writeUint32(pcOffVersionBegin, next)
	m.writeUint32(pcOffLayout, pluginControlLayoutVersion)
	m.writeUint32(pcOffEnableMask, bufAll)
	m.writeUint32(pcOffVersionEnd, next) // commit
}

// graphicsOrWeatherUnsubscribed reports whether the active mask still leaves
// Graphics or Weather unsubscribed, so the caller can warn / retry.
func graphicsOrWeatherUnsubscribed(mask int32) bool {
	return mask&bufGraphics != 0 || mask&bufWeather != 0
}
