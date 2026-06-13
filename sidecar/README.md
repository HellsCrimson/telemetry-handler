# lmu-bridge (telemetry sidecar)

Reads Le Mans Ultimate / rFactor 2 telemetry from the **rF2 Shared Memory Map
Plugin** and re-emits it as a small JSON packet over UDP, so the native
telemetry-handler app can consume it through its existing UDP receiver.

LMU has no native UDP telemetry. On Linux the game runs under Proton/Wine and
its shared memory lives **inside the Wine prefix**, invisible to native Linux
processes. So this binary is built for **Windows** and run under the **same
`WINEPREFIX`** as the game. The UDP it sends to `127.0.0.1` crosses the
Wine→host boundary normally.

## Prerequisites (in the game's prefix)

1. Install TheIronWolf's `rFactor2SharedMemoryMapPlugin64.dll` into
   `Le Mans Ultimate/Plugins/`.
2. Enable it in `Le Mans Ultimate/UserData/player/CustomPluginVariables.JSON`
   (`"Enabled": 1` for the plugin entry).
3. Launch LMU and load into a session so the plugin starts publishing.

## Build

```bash
GOOS=windows GOARCH=amd64 go build -o lmu-bridge.exe ./sidecar
```

(`go build -o lmu-bridge-linux ./sidecar` also compiles on Linux, but the Linux
build is a stub that just errors — it can't see the prefix's shared memory.)

## Smoke test against netcat (no main app needed)

On the Linux host, listen:

```bash
nc -u -l 20440
```

Then run the bridge under the game's prefix:

```bash
WINEPREFIX=/path/to/lmu/prefix wine lmu-bridge.exe -addr 127.0.0.1:20440 -v
```

With LMU running you should see one JSON packet per tick, e.g.:

```json
{"source":"lmu","seq":42,"version":1234,"num_vehicles":1,"vehicle_name":"...","track_name":"...","elapsed_time":12.3,"lap_number":1,"gear":3,"engine_rpm":7200,"speed_ms":58.4,"throttle":0.9,"brake":0,"steering":-0.1,"clutch":0}
```

The bridge also logs a one-line summary to stdout (`-v` logs every packet;
without it, once per second).

## Flags

| flag            | default              | meaning                                          |
|-----------------|----------------------|--------------------------------------------------|
| `-addr`         | `127.0.0.1:20440`    | UDP destination `host:port`                      |
| `-hz`           | `50`                 | poll/send rate (rF2 updates at ~50Hz)            |
| `-map`          | `$rFactor2SMMP_Telemetry$` | telemetry shared-memory object name        |
| `-scoring-map`  | `$rFactor2SMMP_Scoring$`   | scoring buffer (used to find the player)   |
| `-vehicle`      | `-1`                 | force a telemetry vehicle index (`-1` = auto)    |
| `-v`            | off                  | log every packet                                 |
| `-dump`         | `0`                  | hex-dump N bytes of the telemetry buffer once    |
| `-dump-scoring` | `0`                  | hex-dump N bytes of the scoring buffer once      |

The per-packet log line includes `idx=` (the chosen telemetry vehicle index) and
`pid=` (the player's slot id from scoring) so you can confirm it locked onto your
car. If it ever picks the wrong car, force it with `-vehicle N`, or capture
`-dump-scoring 4096` / `-dump 2048` and check the offset constants.

## Notes / next steps

- **Wine UDP:** the bridge sends via raw winsock `socket()`/`sendto()` instead
  of Go's `net` package. Go's UDP setup issues a `SIO_UDP_NETRESET` ioctl that
  Wine doesn't implement, which makes `net.Dial("udp")` fail under Wine/Proton
  with `wsaioctl: winapi error #10045` (WSAEOPNOTSUPP). See `udp_windows.go`.
- **Tear-free reads:** each poll snapshots the buffer and accepts it only when
  the SMMP `mVersionUpdateBegin == mVersionUpdateEnd` counters match.
- **Player car:** the bridge reads the *Scoring* buffer (`$rFactor2SMMP_Scoring$`)
  to find the local player's vehicle (`mControl==0`/`mIsPlayer`), takes its slot
  `mID`, and locates the matching car in the *Telemetry* buffer by `mID` (the two
  arrays are not guaranteed to be in the same order). With AI cars present the
  player is often **not** index 0, which is why reading `mVehicles[0]` blindly
  surfaced another car's inputs. Anything unexpected (scoring missing, no player
  found, id not in telemetry, or a stride mismatch) falls back to `mVehicles[0]`,
  so the worst case is the old behaviour, never garbage. Override with `-vehicle`.
- **Wire format:** JSON on purpose (readable, trivial to parse/evolve). The main
  app gets one new parser branch; the UDP transport stays unchanged. Swap to a
  binary encoding later only if the rate ever justifies it.
- **Struct offsets** are derived from `rF2State.h` (TheIronWolfModding); `long`
  is 32-bit under MSVC. If a future plugin build changes the layout, fix the
  offset constants in `main.go`.
