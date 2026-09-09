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

## Configure `config.json`

Start WagaStrim once to create the file; its path appears in the startup log.
Stop WagaStrim before editing. Merge the fields below into the existing JSON
object, keeping your other settings and `ingests`. Save and restart to apply edits.

### Control access (optional)

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

The connecting app sends the same secret as `Authorization: Bearer YOUR_SECRET`
to the control API on TCP 7333. This is separate from the camera's stream key;
Moblin does not need it. Without `controlToken`, the control API stays off.
With a token, remote dashboards are allowed by default. Set `controlRemote: false`
to refuse internet clients. Restrict TCP 7333 by firewall and use HTTPS or a VPN.

The **Local access** and **Remote access** dashboard toggles apply immediately.
Both off means this machine only. They do not change streaming/preview access or
open firewall/router ports. Set `controlBind` to a LAN or VPN IP to restrict the
listener. The settings webpage remains local on TCP 7330.

Open **API setup guide** in Reachability for token creation, camera key generation,
WHIP/WHEP/preview links and request examples. The guide is included offline at
`http://127.0.0.1:7330/api-guide` ([source](docs/API.html)).

### HTTPS (optional)

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
but support depends on your sender and receiver. New cameras start at 2000 ms
delay, adjustable from 0 to 10000 ms. Experimental Waga bonding and packet recovery need
a compatible sender; ordinary WHIP senders can still connect.

[Transport and test notes](docs/TRANSPORT.md) | [MIT license](LICENSES/MIT.txt)
