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

## Optional HTTPS

HTTP remains the default. It sends stream keys without encryption; use HTTPS
for public internet connections, or carry HTTP through an encrypted VPN.

To enable HTTPS, stop WagaStrim and edit the `config.json` file shown in its
startup log. Add `tlsCert` and `tlsKey` with the paths to your certificate chain
and private key, and set `publicHost` to the hostname covered by the certificate:

```json
"publicHost": "stream.example.com",
"tlsCert": "/path/to/fullchain.pem",
"tlsKey": "/path/to/privkey.pem"
```

Restart WagaStrim. The sender link changes to `whips://` and the receiver link to
`https://`, on the same signaling port. Other WHIP clients use `https://`.
Use a certificate trusted by the phone and browser. Remove both TLS fields to
return to HTTP. Restart after certificate renewal to load the new files.

If a local reverse proxy handles HTTPS, leave those TLS fields unset and add
`"signalBind": "127.0.0.1"` and `"publicUrl": "https://stream.example.com"`.
The proxy must reach WagaStrim locally; forwarding headers never grant trust.
For a VPN, `signalBind` can instead be the machine's VPN IP.

The optional control API accepts token-authenticated connections from local
Wi-Fi, private VPN addresses and loopback. Public source addresses are rejected
unless **Remote access** is enabled in the dashboard.
Connect to the server's LAN IP on TCP 7333; allow that port from your LAN in the
host firewall. Set `controlBind` to a particular LAN or VPN IP to restrict its
listener further. Direct TLS also applies to this API. Forward this port only
when deliberately enabling remote control. The settings page on TCP 7330 remains local.
The dashboard's **Local network access** toggle enables or disables LAN/VPN
control requests immediately and saves the choice. It requires a configured
control token and does not change camera ingest or preview access.
**Remote access** is a separate toggle, off by default, saved as `controlRemote`.
It permits token-authenticated internet control requests immediately. Use HTTPS
for remote control; the toggle does not configure certificates, bind addresses,
router forwarding or host firewall rules. Turning both access toggles off keeps
control requests local to this machine.

HTTP requests have time and rate limits. WHEP allows up to 8 subscribers per
camera and 64 overall, counting negotiations; unfinished connections expire.

## Compatibility

Audio follows the sender automatically: **Opus or experimental AAC**. AAC is
forwarded unchanged and needs an AAC-capable WHEP receiver; the built-in player
and OBS Browser Source need **Opus**. No audio converter or FFmpeg is included.

H.264 is the simplest video choice. H.265 and AV1 can be enabled per camera,
but support depends on your sender and receiver. New cameras start at 2000 ms
delay, adjustable from 0 to 10000 ms. Experimental Waga bonding and packet recovery need
a compatible sender; ordinary WHIP senders can still connect.

[Architecture and test notes](PLAN.md) | [MIT license](LICENSES/MIT.txt)
