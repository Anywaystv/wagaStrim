![WagaStrim banner](wagastrimheader.png)

Send a phone camera to OBS over WHIP and WHEP. WagaStrim runs on your server or computer,
buffers the incoming stream, and forwards it without re-encoding.
Built with [Pion WebRTC](https://github.com/pion/webrtc).

## Install

Build from source on macOS, Windows or Linux. Install
[Go 1.26.6 or newer](https://go.dev/dl/), download this repository using
**Code > Download ZIP**, and extract it. Open a terminal in the extracted folder:

```sh
go build -tags notray -o bin/ ./cmd/wagastrim
```

Start it with the command for your system:

| System | Command |
| --- | --- |
| macOS / Linux | `./bin/wagastrim -headless` |
| Windows PowerShell | `.\bin\wagastrim.exe -headless` |

Open **http://127.0.0.1:7330** on the same machine (use an SSH tunnel for a remote server).
Keep the terminal open while streaming;
Ctrl+C stops the server. This build uses the web settings page, without a tray icon.

## Connect your camera

1. Add a camera in WagaStrim. Start with **H.264 video and Opus audio** in Moblin.
2. Copy the **sender link** into Moblin. On the same Wi-Fi, replace the link's
   host with a local IP shown by **Check**, keeping its port and key.
3. Copy the **receiver link** into an OBS **Browser Source**. Enable
   **Control audio via OBS** and disable **Shutdown source when not visible**.

Allow these ports through your server or computer's firewall. For a phone on cellular,
use the public IP and, if behind a router, forward the ports to your server or computer:

| Port | Purpose |
| --- | --- |
| TCP 7331 | WHIP/WHEP connection setup |
| UDP 7332 | Live audio and video |

The settings page on TCP 7330 stays local; do not forward it. **Check** finds
addresses but cannot confirm port forwarding. Test with phone Wi-Fi turned off.
Keep your stream keys and public IP off your broadcast.

## Compatibility

Audio follows the sender automatically: **Opus or experimental AAC**. AAC is
forwarded unchanged and needs an AAC-capable WHEP receiver; the built-in player
and OBS Browser Source need **Opus**. No audio converter or FFmpeg is included.

H.264 is the simplest video choice. H.265 and AV1 can be enabled per camera,
but support depends on your sender and receiver. The default delay is 2 seconds,
adjustable up to 10 seconds. Experimental Waga bonding and packet recovery need
a compatible sender; ordinary WHIP senders can still connect.

[Architecture and test notes](PLAN.md) | [MIT license](LICENSES/MIT.txt)
