# SSH Jump — ACAP for Axis cameras

A small ACAP application that runs on an Axis device (which is on the same
network as the equipment you need to reach) and provides a **browser-based SSH
terminal**, served inside the camera's own web interface — just like the AOA/AMD
configuration pages. It acts as a **jump host**: from anywhere you can open the
camera's web UI, you can SSH into devices on the local network (for example, to
re-adopt a UniFi switch over SSH).

For security, the app **stops itself after 30 minutes of inactivity** (no real
terminal I/O). To use it again, simply re-enable it.

```
your browser ──HTTPS──> Axis camera web UI ──SSH──> switch / device on the LAN
                        (reverse proxy + Axis authentication)
```

## Why HTTP long-poll (not WebSocket)

The AXIS OS Apache reverse proxy strips the WebSocket upgrade headers, so a
`ws://` connection cannot survive the proxy hop. The terminal therefore uses a
plain HTTP transport: `POST api/connect` opens the session, `GET api/poll`
long-polls for output, `POST api/input` sends keystrokes, and
`POST api/resize` / `api/close` manage the rest. This works through any HTTP
proxy.

## Contents

```
app/                 ACAP source
  main.go            backend: HTTP server + terminal<->SSH bridge + idle watchdog
  html/index.html    terminal (xterm.js) + connection form
  manifest.json      reverseProxy, settingPage, runMode "once"
  vendor/            vendored Go dependencies (offline, no-network build)
  LICENSE            application license + third-party licenses
build.sh             cross-compile and package the .eap (no Docker)
pack-eap.sh          package an .eap from a prebuilt binary
Dockerfile           canonical build with the official Axis ACAP Native SDK
sshjump_1_0_0_aarch64.eap    prebuilt (ARTPEC-8 and newer cameras)
sshjump_1_0_0_armv7hf.eap    prebuilt (ARTPEC-7 and older)
```

## 1. Which .eap to use

Depends on the camera SoC:

- **aarch64** — ARTPEC-8 and most recent cameras.
- **armv7hf** — ARTPEC-7 and older models.

If unsure, try `aarch64` first; if the camera rejects it due to an architecture
mismatch, use `armv7hf`.

## 2. Install on the camera

1. Open the camera web UI → **Apps**.
2. **IMPORTANT (AXIS OS 11+):** installation requires signed apps *or* that you
   allow unsigned apps. These `.eap` files are **not signed**. On the **Apps**
   page, enable **"Allow unsigned apps"**. Alternatively, sign the `.eap` (see
   section 5).
3. Choose **Add app** / manual install and upload the `.eap`.
4. **Enable** the app (the toggle). With `runMode: once`, this is when the
   server starts; when the watchdog stops it on inactivity, it stays stopped
   until you re-enable it.

## 3. Usage

1. On the app page, open the **settings page** (opens the terminal).
2. Fill in:
   - **Host / IP** of the target device (the switch's LAN IP),
   - **Port** (22 by default),
   - **Username** and **password** (or a **private key**).
3. Click **Connect**. The host key fingerprint is shown for you to verify, and
   the terminal opens.

To **re-adopt a UniFi switch**, once you have an SSH session run something like:

```
set-inform http://YOUR-CONTROLLER-IP:8080/inform
```

(the switch's SSH credentials are the ones set in your UniFi controller; confirm
the exact command for your firmware version).

## 4. Security / auto-stop

- The backend listens only on `localhost`; all external access goes through the
  camera's Apache reverse proxy, reusing Axis **authentication and TLS**. The
  `access` policy is set to `admin` in `manifest.json`.
- **30-minute inactivity:** "inactivity" means **no real terminal I/O** (no
  keystrokes and no output) for 30 minutes. Simply keeping the page open, or an
  open-but-idle SSH session, does **not** count as use — after 30 minutes with
  no I/O the app terminates the process and closes the session(s). With
  `runMode: once` it is not restarted, so the jump host is unavailable until you
  re-enable the app. The countdown in the top-right of the page shows the time
  remaining and counts down while the terminal is idle (it resets on keystrokes
  or output).
- Change the timeout with the `SSHJUMP_IDLE_TIMEOUT` environment variable
  (e.g. `15m`, `1h`).

## 5. Signing the app (optional on OS 11/12, required on OS 13)

The ⚠️ "this application does not have a valid signature" warning appears
because the `.eap` is built by you and not signed by Axis. On **AXIS OS 11 and
12** you only need to enable "Allow unsigned apps". From **AXIS OS 13** onward
only signed apps are accepted. To sign (free, requires an axis.com account):

1. As an individual (non-TIP) developer, use the **ACAP Signing Service**:
   developer.axis.com → *How-to guides* → *Service portal* →
   *ACAP application signing tool*.
2. The service requires **manifest schema 2.x**. This project uses 1.3.0, so to
   sign, bump `schemaVersion` to `"2.0.0"` in `app/manifest.json` (the
   `acapPackageConf` structure stays the same) and copy in the `vendor` and
   `vendorId` the service gives you.
3. Rebuild the `.eap` (`./build.sh`).
4. Log in to the service, accept the terms, upload the `.eap`, and click sign.
5. Download the **signed** `.eap` and install that one — the warning disappears.

Docs: https://developer.axis.com/acap/how-to-guides/service-portal/acap-application-signing/

## 6. Build it yourself (optional)

**Without Docker** (needs Go ≥ 1.21; uses the vendored deps in `app/vendor`, no
network required):

```
./build.sh            # builds both .eap files
./build.sh aarch64    # or just one
```

**With the official Axis SDK** (package format identical to Axis'; needs Docker
with Docker Hub access):

```
DOCKER_BUILDKIT=1 docker build \
  --build-arg GOARCH=arm64 --build-arg SDK_ARCH=aarch64 \
  -o type=local,dest=./out .
```

(for armv7hf: `--build-arg GOARCH=arm --build-arg GOARM=7 --build-arg SDK_ARCH=armv7hf`).
The `.eap` is written to `./out/`.

## Technical notes

- Authentication supported: password, keyboard-interactive (common on network
  gear), and private key (with optional passphrase).
- Host key verification is *trust-on-use* (the fingerprint is shown rather than
  checked against a known_hosts file), as this is an interactive administration
  tool for a trusted LAN.
- The backend is a single static Go binary (no CGO), cross-compiled for
  `aarch64` and `armv7hf`.

## License

See [`app/LICENSE`](app/LICENSE) — the application license plus the full text of
all bundled third-party open-source licenses.

Not affiliated with, endorsed by, or sponsored by Axis Communications AB or
Ubiquiti Inc. "Axis", "AXIS OS", "ACAP", "ARTPEC", "UniFi" and "Ubiquiti" are
trademarks of their respective owners.
