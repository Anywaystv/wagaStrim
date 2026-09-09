![WagaStrim banner](wagastrimheader.png)

# WagaStrim (Experimental)

Send a phone camera to OBS over WHIP and WHEP. WagaStrim runs on your server or computer,
buffers the incoming stream, and forwards it without re-encoding.
Built with [Pion WebRTC](https://github.com/pion/webrtc).

WagaStrim is experimental. Test your sender, receiver and network before relying
on it for a live broadcast.

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

Open **http://127.0.0.1:7330** on the same machine, or use an SSH tunnel for a remote
server. Keep the terminal open while streaming; Ctrl+C stops WagaStrim.
This build uses a web dashboard instead of a tray icon.

## Connect your camera

1. Add a camera in WagaStrim. Start with **H.264 video and Opus audio** in Moblin.
2. Copy the **sender link** (WHIP) into Moblin. On the same Wi-Fi, replace the link's
   host with a local IP shown by **Check**, keeping its port and key.
3. Copy the **receiver link** (the WHEP preview page) into an OBS **Browser Source**. Enable
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

## Optional configuration

WagaStrim creates `config.json` on first start and logs its location. Stop the
server before editing it. Add the fields below to the existing JSON object,
preserving your other settings and `ingests`, then save and restart.

### Control API

The control API lets another app manage cameras and settings. It is not needed
to publish from Moblin or watch a stream.

In **Reachability**, click **Generate control token**, save it in a password
manager, then restart. Or set a password-manager-generated secret of at least
32 characters in the existing config:

```json
{
  "controlToken": "REPLACE_WITH_YOUR_RANDOM_SECRET",
  "controlLan": true,
  "controlRemote": true
}
```

The connecting app sends the secret as `Authorization: Bearer YOUR_SECRET`
to TCP 7333. This token is separate from camera stream keys.
Without `controlToken`, the control API stays off.
With a token, remote dashboards are allowed by default. Set `controlRemote: false`
to refuse internet clients. Restrict TCP 7333 by firewall and use HTTPS or a VPN.

The **Local access** and **Remote access** dashboard toggles apply immediately.
Both off means this machine only. They do not change streaming/preview access or
open firewall/router ports. Set `controlBind` to a LAN or VPN IP to restrict the
listener.

Click **API Guide** to the right of the live-status indicator in the top bar for
token setup, camera keys, streaming links and request examples. It is available offline at
`http://127.0.0.1:7330/api-guide` ([source](docs/API.html)).

### HTTPS

HTTP is the default and exposes keys/tokens to anyone observing the connection.
For HTTPS, add these fields using your hostname and trusted certificate files:

```json
{
  "publicHost": "stream.example.com",
  "tlsCert": "/path/to/fullchain.pem",
  "tlsKey": "/path/to/privkey.pem"
}
```

Restart. Moblin links use `whips://`; receiver links and other WHIP clients use
`https://`. Ports stay the same; TLS also covers the control API. Restart after
certificate renewal. Remove both TLS fields to return to HTTP.

For a local HTTPS reverse proxy, omit the TLS fields and set `signalBind` to
`127.0.0.1` and `publicUrl` to `https://stream.example.com`. Use HTTPS or an
encrypted VPN for internet access.

## Compatibility

Audio follows the sender automatically: **Opus or experimental AAC**. AAC is
forwarded unchanged and needs an AAC-capable WHEP receiver; the built-in player
and OBS Browser Source need **Opus**. No audio converter or FFmpeg is included.

H.264 is the simplest video choice. H.265 and AV1 can be enabled per camera,
but both the sender and receiver must support the chosen codec over WebRTC.

New cameras start with a 2000 ms buffer, adjustable from 0 to 10000 ms. More
buffer can absorb short interruptions at the cost of extra viewing delay.
It cannot compensate for a connection that is consistently too slow.

Optional Waga bonding and packet recovery require a compatible sender, such as
Moblin with WagaWebRTC. Ordinary WHIP senders can connect without those features.

[Transport and test notes](docs/TRANSPORT.md) | [MIT license](LICENSES/MIT.txt)
