# yarr

**yarr** (yet another rss reader) is a web-based feed aggregator which can be used both
as a desktop application and a personal self-hosted server.

The app is a single binary with an embedded database (SQLite).

![screenshot](etc/promo.png)

## usage

The latest prebuilt binaries for Linux/MacOS/Windows are available
[here](https://github.com/nkanaev/yarr/releases/latest).
The archives follow the naming convention `yarr_{OS}_{ARCH}[_gui].zip`, where:

* `OS` is the target operating system
* `ARCH` is the CPU architecture (`arm64` for AArch64, `amd64` for X86-64)
* `-gui` indicates that the binary ships with the GUI (tray icon), and is a command line application if omitted

Usage instructions:

* MacOS: place `yarr.app` in `/Applications` folder, [open the app][macos-open], click the anchor menu bar icon, select "Open".

* Windows: open `yarr.exe`, click the anchor system tray icon, select "Open".

* Linux: place `yarr` in `$HOME/.local/bin` and run [the script](etc/install-linux.sh).

[macos-open]: https://support.apple.com/en-gb/guide/mac-help/mh40616/mac

For self-hosting, see `yarr -h` for auth, tls & server configuration flags.

See more:

* [Building from source code](doc/build.md)
* [Fever API support](doc/fever.md)

## Modifications in this fork

This fork includes several improvements and customizations:

### UI/UX Enhancements
- **Grey Theme**: Added a new grey theme option replacing the sepia theme
- **Item List Display**: Improved styling and layout for better readability
- **Feed Icons**: Display feed icons in the item list with fallback for missing icons
- **Date Display**: Replaced relative time with formatted date display
- **Middle-click Support**: Added ability to open items in new tabs with middle-click

### Feed Management
- **Manual Refresh Control**: Added manual refresh button and ability to stop ongoing refresh operations
- **Responsive Layout**: Adjusted default layout constraints for better responsiveness across devices

### Configuration Changes
- **Default Port**: Changed default server port from 7070 to 7777 for better port allocation flexibility
- **UI Defaults**: Updated default UI layout and theme settings

### Docker Support
- **Docker Compose**: Added docker-compose configuration for easier deployment

## credits

[Feather](http://feathericons.com/) for icons.
