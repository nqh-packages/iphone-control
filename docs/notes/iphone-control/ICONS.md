# Icon guesses in a screen read

A WDA read of the iPhone shows, after the tree and OCR lines, guesses at icons the tree does not name:

```text
screen 375x667 · app: Safari (com.apple.mobilesafari) · portrait · keyboard hidden
[1] button "Tabs" @340,640
ocr "More information..." @120,300
icon? "close" @352,40
```

A guess has no ref, may be wrong and is tapped by its point. `icons unread` in the header means the detector did not answer within 350 ms, so a missing line says nothing. Without an install, a read is the same as without this feature.

## How it works

- `internal/screen` holds the guesses in unexported fields: `Render` and `Diff` show them, and no other package can read them. A diff removes a guess only when both reads looked, and prints nothing when a box moves by less than half its smaller side. Guesses under hidden or masked elements are not shown.
- `internal/icons` does the rest. `Look` sends the read's capture-gated picture to a child process (the same program, `phone icons serve <dir>`), which runs ONNX Runtime through purego, so a crash, hang or leak costs only the icons. A request unanswered for 3 s kills the child; it restarts after 1 s, doubling to 60 s. `Found.For` then drops guesses for another app, the weaker of two overlapping ones, and those on the keyboard or in a labelled tree element smaller than a sixth of the screen.
- The driver (`internal/driver/ios/wda_device.go`) looks beside OCR, on the same picture, and drops the guesses whenever it drops OCR's text.
- The policy, refs and the interrupt book never see a guess. A tap on one is checked against the tree and the text OCR reads there, like any tap at a point.
- iPhones read without the helper (iOS 27 inspector path) and Android have no guesses.

## Install, status, remove

```sh
pinkyctl phone icons install models/icons/advnt-screen-parser-v4.json
bin/iphone-daemon start
pinkyctl phone icons status
```

`install` downloads the model, its class names and the runtime wheel, checks each sha256, keeps only the runtime library out of the wheel, and tries the detector on a blank picture before it records the install in `<data dir>/icons/active`. The daemon reads the install once, at its start. `phone status` and `phone icons status` check that every file is there with its size; the child checks the model's and the library's sha256 as it loads them, and the daemon log names a file that differs. To remove the detector, delete `<data dir>/icons/` and restart the daemon.

The model and its runtime are data: `models/icons/*.json` names them, their classes, labels, thresholds and apps. No model file is committed, embedded or shipped (`TestNoWeightsInRepo`). The `advnt-screen-parser` code is MIT, its weights AGPL-3.0; they are fetched to this Mac only.

## Looking at what it sees

- `pinkyctl phone icons` reads the phone and prints the header and the guesses.
- `pinkyctl phone icons <picture> [-app ID] [-all]` runs a saved picture and writes nothing; `-all` also prints each dropped detection with its reason. It has no element tree, so it does not drop what the tree names.

## Thresholds and app scope

A calibration, not a measured rule: the values were fitted on 118 frames of this one phone and judged on the same frames. Settings, Safari, App Store, Messages and system sheets were in the set. No TikTok, Instagram, X or YouTube screen was.

| Class | Label | Least score | Apps |
| --- | --- | --- | --- |
| `ios_close` | close | 0.60 | any |
| `ios_clear_search` | clear search | 0.35 | any |
| `yt_search`, `tt_search` | search | 0.35 | any |
| `tt_back`, `ig_back` | back | 0.35 | any |
| `tt_menu`, `ig_menu` | menu | 0.35 | any |
| `ios_app_*` | | off | the home screen's tree names app icons |
| every other class | name without its app prefix | 0.35 | its own app only |

On that set these rules keep 51 of 66 detections, 2 of them wrong in meaning (a back chevron and a checkmark read as close, at 0.76 and 0.77). Recall is not measured: most unlabelled system icons were missed. Guesses near 0.35 may come and go between reads. The model takes portrait pictures only; a landscape screen has no guesses. The pictures measured were 375x667 JPEG frames (Wi-Fi); the 750x1334 screenshot a USB read uses is not measured.

To re-measure: save screens, run `pinkyctl phone icons <picture> -all` on each, judge each detection by eye, and change `min_score`, `apps` or `off` in the manifest; install it again and restart the daemon.

## Measured

On this Mac (Apple silicon, load average about 20), `PINKY_ICONS_REAL=1 PINKY_ICONS_PARITY=<detections> go test ./internal/icons -run RealModel -v`, 2026-11-26:

| What | Result |
| --- | --- |
| 20 looks through the child | p50 149 ms, p95 156 ms |
| Detections matched against the Python detector (class and box within 3 px, 118 frames) | 64 of 66, score difference at most 0.076 (bilinear resize here, OpenCV's there) |
| Idle child | 0 s of CPU in 1 s, no network sockets |

The parity file lists `{"file", "icons": [{"label", "confidence", "box": [x1,y1,x2,y2]}]}` per saved 375x667 picture.

## Differences from the design

- The child returns every detection above the global least score after per-class overlap suppression, marked with why it is dropped (class threshold, `off`), so `-all` can explain them.
- `Explain` also returns an error, so a detector that cannot run does not read as one that found nothing.
- `Found` also carries the manifest's orientation and overlap limit, which `Found.For` applies.
- A picture of an orientation the model does not read is not sent to it.
