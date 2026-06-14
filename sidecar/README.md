# lmu-bridge (telemetry sidecar)

Reads Le Mans Ultimate / rFactor 2 telemetry from **all** of the **rF2 Shared
Memory Map Plugin**'s buffers and re-emits it as a chunked binary frame over UDP,
so the native telemetry-handler app can consume it through its existing UDP
receiver.

Each frame carries **full telemetry for every car** (engine, per-wheel
tires/suspension/forces, aero, damage, electric boost, …), the **full per-car
scoring** (positions, lap/sector times, gaps, pit state, flags, names) and the
**session globals** (weather, track rules / safety car, driving aids, pit speed
limit). A full grid exceeds the 64KB UDP datagram limit, so a frame is split
into chunked datagrams the app reassembles (see `telemetry-handler/game/lmu/wire`).

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
3. **Subscribe to all buffers.** The plugin leaves **Graphics (bit 32)** and
   **Weather (bit 128)** *unsubscribed by default*, so those buffers never
   update until you subscribe. Set, in the same plugin entry:

   ```json
   "UnsubscribedBuffersMask": 0
   ```

   The bridge **also** requests this at runtime by writing the `PluginControl`
   buffer (best-effort; only works when the plugin has `PluginControlInput`
   enabled), and logs a warning while Graphics/Weather remain unsubscribed — but
   the static config above is the reliable fix. Pass `-no-subscribe` to skip the
   runtime write.
4. Launch LMU and load into a session so the plugin starts publishing.

## Build

```bash
GOOS=windows GOARCH=amd64 go build -o lmu-bridge.exe ./sidecar
```

(`go build ./sidecar` also compiles on Linux, but the Linux build is a stub that
just errors — it can't see the prefix's shared memory.)

## Smoke test

The wire is **binary** now (no longer human-readable JSON), so `nc` will show
raw bytes. The simplest check is to run the bridge with `-v` and watch its
per-frame summary, or run the main app and watch the dashboard:

```bash
WINEPREFIX=/path/to/lmu/prefix wine lmu-bridge.exe -addr 127.0.0.1:20440 -v
```

With LMU running you should see one summary line per frame, e.g.:

```
seq=1234 cars=20 playerIdx=7 car="Ferrari 499P" payload=37896B chunks=1
```

`-v` logs every frame; without it, once per second.

## Flags

| flag             | default              | meaning                                              |
|------------------|----------------------|------------------------------------------------------|
| `-addr`          | `127.0.0.1:20440`    | UDP destination `host:port`                          |
| `-hz`            | `50`                 | poll/send rate (rF2 updates at ~50Hz)                |
| `-vehicle`       | `-1`                 | force a telemetry vehicle index (`-1` = auto)        |
| `-chunk`         | `0`                  | max UDP payload bytes per chunk (`0` = ~60000)       |
| `-no-subscribe`  | off                  | don't write PluginControl to enable Graphics/Weather |
| `-v`             | off                  | log every frame                                      |
| `-dump`          | `0`                  | hex-dump N bytes of the telemetry buffer once        |
| `-dump-scoring`  | `0`                  | hex-dump N bytes of the scoring buffer once          |
| `-dump-extended` | `0`                  | hex-dump N bytes of the extended buffer once         |
| `-logfile`       | `""`                 | also tee logs to a file (Windows path under Wine)    |

The shared-memory object names are the plugin defaults (`$rFactor2SMMP_*$`) and
are no longer configurable via flags.

The per-frame log includes `playerIdx=` (the chosen vehicle) so you can confirm
it locked onto your car. If it picks the wrong car, force it with `-vehicle N`,
or capture `-dump-scoring 4096` / `-dump 2048` and check the offsets.

## Notes / internals

- **Wire format:** binary, defined once in `telemetry-handler/game/lmu/wire` and
  shared by the sidecar (encoder) and the app (decoder), so the two ends can't
  drift. The per-vehicle structs mirror the rF2 `pack(4)` C structs
  field-for-field (those have no implicit padding — alignment gaps are explicit
  expansion arrays — so Go's tightly-packed `encoding/binary` reads them
  byte-for-byte). Struct sizes (1888/584/548/260) are asserted in tests.
- **Chunking:** `wire.Chunk` splits a frame into datagrams each prefixed with a
  24-byte envelope (`LMU2` magic, frame seq, chunk index/count, total length,
  offset). `wire.Reassembler` stitches them back; a newer frame supersedes an
  incomplete older one.
- **Buffers read:** Telemetry, Scoring, Rules, Weather, Graphics, ForceFeedback,
  PitInfo, Extended. Telemetry is required; the rest are best-effort (a missing
  optional buffer just leaves its section zero-valued).
- **Tear-free reads:** the versioned buffers (Telemetry/Scoring/Rules) are
  accepted only when `mVersionUpdateBegin == mVersionUpdateEnd`.
- **Player car:** read from the *Scoring* buffer (`mControl==0`/`mIsPlayer`); its
  slot `mID` is matched against the *Telemetry* buffer (the two arrays aren't
  necessarily in the same order). Anything unexpected falls back to vehicle 0.
- **Wine UDP:** the bridge sends via raw winsock `WSASendTo` instead of Go's
  `net` package, which fails under Wine on a `SIO_UDP_NETRESET` ioctl. See
  `udp_windows.go`.
- **Struct offsets** are derived from `rF2State.h` (TheIronWolfModding); `long`
  is 32-bit and pointers/`ULONGLONG` align to 4 under MSVC `pack(4)`. If a future
  plugin build changes the layout, fix the mirror structs in `game/lmu/wire` and the
  prefix/offset constants in `rf2.go`/`rf2_extended.go` (the size-assertion tests
  will flag a mismatch).
```
