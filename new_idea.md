What if we change how we think about this.

Instead

two pieces.

GoLang app (mac, linux, windows) - arm64, amd64
- "sbox"
- Homebrew
- Version numbered
- Single binary
   - Includes Dockerfile definition
   - Includes Linux amd64/arm64 binaries for the internal app
- built-in tailscale integration (optional)

sbox
 - starts sbox with root folder = cwd
 - parses .slopbox
 - builds sb-[ver] if DNE from embedded Dockerfile
 - creates container

Container does what it's currently doing.  Connects to docker service, somehow, and handles port mapping automatically. (either to localhost or tailnet IP)
 - so ports from the slopbox.toml files OR
 - discovered ports get a checkbox maybe to map (or maybe it's auto)

 Keep the /p/ stuff becomes it's handy for me on an iPad or something, but this becomes the primary mechniams.

 GHA can then just build the app.  We don't really need to build the image.


So that port mapping is atuomaticl.  Tialscale is trivial.  The UI you are building is looking pretty good otherwise.

Probably don't need environment variables.

sb
sb --no-auth

Not sure where to store Tailscale stuff.  Open to ideas.  I don't want to have to .gitignore a bunch of stuff in the project directories.

Ooooh.  What if sb is a multi project coordinator.

so
cd project1 && sb (starts sb, and registers worker container for project1)
cd project2 && sb (starts secondary worker for proejct2 - keep em separated, and rewg with existing sb)

So sb requires only one port.
Can coorinates worker ports.  One UI for all active work.

OMG yes.

Each project lives in a bit of a sandbox.  Annoyingly claude config, etc. aren't shrewad but maybne that's good