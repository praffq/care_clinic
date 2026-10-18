# Using CARE Clinic

[Documentation index](README.md)

This guide follows the desktop application from first launch through setup,
connection and the server control panel. The app is only the user interface:
on a server, the clinic runs in the background `care-clinic-service` and keeps
running when the app is closed.

The desktop opens at 1100 by 700 and supports a minimum window of 720 by 560.
The interface uses plain language for failures; **Open log** provides
diagnostic detail for support.

## First launch and saved roles

The Start screen offers **Start setup** and **Connect to an existing server on
the local network**. Viewing this screen saves no role.

| Choice | When it is saved | What Back can do |
| --- | --- | --- |
| Host the clinic | `BeginServerSetup` records Server when the setup screen opens. | Leave setup while nothing has been built on the choice. |
| Connect to a clinic | `ConnectClient` records Client when connecting begins. Finding a clinic does not save it. | Leave before connecting. After a connection, use Disconnect. |

An existing installation resumes its saved role. See
[configuration and role guards](configuration-and-settings.md#persisted-config).

## Set up this computer as the server

Server setup needs a Mac with Apple silicon; it is not available on Windows
yet. The full sequence is in [server onboarding](onboarding.md).

1. **Computer checks.** CARE checks virtualization, memory, free disk space
   and the CARE Clinic service. Each row shows its result and detail; warnings
   don't block. If the service is missing, **Install service** shows one macOS
   administrator prompt, then the checks run again. If CARE then asks you to
   log out and back in, do so and reopen CARE Clinic. **Continue** is enabled
   once nothing fails.
2. **Clinic address.** Choose the local clinic name. CARE validates it and
   checks for another visible server using it. Keep other clinic servers awake
   during setup: an offline computer cannot be detected.
3. **Admin password.** Choose the CARE Clinic password: 8 to 20 characters
   with an uppercase letter, a lowercase letter and a digit. It also becomes
   the password of the CARE web login `admin`. Save it in a password manager;
   there are no recovery codes.
4. **Install.** CARE installs the engine if needed, then the clinic, and
   streams progress from the log. The bar is indeterminate. Success opens the
   control panel; at the end CARE offers to set up this computer's browser for
   `https://<name>.local/`.

If installation fails, **Try again** resumes the same attempt while CARE
Clinic stays open. Otherwise CARE removes the unfinished clinic and setup
starts over. The service and engine stay installed.

### Permission prompts

CARE-owned setup, saved-address removal and certificate-removal confirmations
use the in-window dialog: a short explanation, **Continue**, and **Cancel**.
Cancel initially has focus; Escape declines. The dialog does not collect an
operating-system password.

The next prompt may come from macOS, Windows or Linux itself. Enter the
computer's administrator password or approve the system request there. macOS can
require a separate certificate security approval. These native prompts are not
replaced or bypassed by the desktop dialog.

The effect of declining depends on the operation. Optional setup for opening
CARE on the server computer can be skipped without stopping the clinic.
Incomplete removal is reported and remains retryable; it is not silently marked
clean. [Native integration details](native-integrations.md#administrator-approval-and-local-hosts-setup)
explain which changes are attempted and verified.

## Connect another computer

Use the clinic address shown on the server and a trusted clinic network.

1. Choose **Connect to an existing server on the local network**, enter the
   clinic name, and choose **Find clinic**. This discovery step installs nothing.
2. Review the found clinic, then choose **Connect**. If an old address override
   prevents discovery, **Connect and fix** is the explicit repair path.
3. Approve the computer's permission requests when needed. Connecting removes
   conflicting CARE-owned trust and address settings, installs the clinic's
   certificate and verifies the secure connection before opening CARE.
4. Use **Open CARE** from the connected screen. Its periodic availability check
   is read-only. Offline or verification failures do not erase the saved
   connection; use the displayed retry or repair guidance.

Connect is the consent step; it does not add another CARE confirmation in front
of the operating system's approval. A partially completed connection retains
enough information to retry or **Disconnect**. Disconnect removes this client's
owned access, not the server's records. Certificates not installed by this
client are preserved.

Initial certificate bootstrap is trust on first use over local HTTP. The
automatic TLS checks do not rule out impersonation on an untrusted network.
See [client trust](native-integrations.md#native-client-setup-and-trust-on-first-use)
and [earlier-installation recovery](client-recovery.md).

## Server control panel

**Overview** shows the service summary (title, detail and any notes, such as a
name conflict or a port another program holds) and details: engine state and
version, clinic stack state, the advertised `<name>.local` address, and the
forwarded ports. It updates live from `service-status`. If the service can't
be reached, Overview says so with the error.

**Start**, **Stop** and **Restart** need no password and are disabled while
another job runs. Stop keeps all data. The service starts the clinic again
after a reboot without the app.

| Tab | What to use it for |
| --- | --- |
| Overview | Clinic status, Start/Stop/Restart, opening CARE, phone/tablet connection, network fix. |
| Home access | Tailscale setup, turning home access on or off and doctor sharing. See [Home access](home-access.md). |
| Updates | CARE Clinic app updates and the update-check switch. |
| Advanced | Clinic settings, CARE Clinic password, diagnostic log and uninstall. |

**Connect phone or tablet** displays a QR code for
`http://<clinic>.local/setup`. Phones and tablets still need to follow their
platform's certificate instructions.

Backups, plugins, storage, CARE software updates, web password reset and
recovery codes were removed in v2; they return later via engine agent ops.

### Advanced

Advanced asks for the CARE Clinic password and locks again **15 minutes after
unlocking**, or sooner when leaving the tab. This is a frontend timer; every
protected Go method checks the password again.

Clinic settings edit the installed `backend.env` and `frontend.env`; see
[configuration](configuration-and-settings.md#environment-file-api). Email,
SMS and MFA settings are not offered.

### App updates

A CARE Clinic update downloads a verified application installer or replaces
the app bundle. Conflicting actions stay locked while it runs:

| Desktop update state | How to proceed |
| --- | --- |
| Checking, available, or failed check | Continue ordinary work, retry the check, or explicitly start the update. |
| Downloading, verifying or installing | Keep CARE Clinic open and wait. |
| External installer opened | Follow the installer. After the native handoff completes, **OK** or **Done** acknowledges the handoff and releases the interface lock. |
| Reopening | Wait for the application to reopen. There is no OK/Done action that releases this state. |
| Failed download or installation | Read the short failure message and retry when ready. |

A handoff acknowledgement does not prove the external installer finished and
does not change the running version to "Up to date".

## Closing and removing CARE

Closing during a job asks whether to quit or **Keep waiting**; it never means
the job succeeded. Closing an idle desktop just quits; the clinic keeps
running. On macOS closing the window hides it.

Server removal is in Advanced and requires the CARE Clinic password. One
choice, **Also delete clinic data (database and files)**, decides whether the
clinic's data goes too. A progress message stays until both `uninstalled` and
the matching successful `care-done` arrive; a failure keeps removal available.
Removing the desktop app is a separate optional step. The service and engine
are not removed. See [cleanup and uninstall](cleanup-and-uninstall.md).
