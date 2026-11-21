## dot git transport

Use HTTPS for GitHub while SSH to GitHub is blocked

### Synopsis

Probe SSH and HTTPS to GitHub and point GitHub remotes at the transport
that works. The choice is written to ~/.config/git/github-transport.inc,
which the managed git config includes when a GitHub user is configured.

ssh mode rewrites https://github.com/ to git@github.com: (the default).
https mode rewrites git@github.com: and ssh://git@github.com/ to
https://github.com/, so fetch and push go through the configured
credential helper (for example gh auth git-credential).

SSH counts as usable only when GitHub authenticates the key. HTTPS counts
as usable when github.com is reachable and git can fill a credential for
it without a prompt. With SSH usable the mode is ssh; with only HTTPS
usable it is https; with neither the current mode is kept.

Git cannot switch transport in the middle of a fetch, so the mode changes
only when this command runs: by hand, or every interval through
dot git transport setup on macOS.

```
dot git transport [flags]
```

### Examples

```
  dot git transport
  dot git transport --check --json
```

### Options

```
      --check[=true|false]   probe and report without writing the mode file
  -h, --help[=true|false]    help for transport
      --json[=true|false]    print the probe report as JSON
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --verbose[=true|false]   show library and debug logs on stderr
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot git](dot_git.md)	 - Git settings dot manages beyond the config file
* [dot git transport setup](dot_git_transport_setup.md)	 - Run dot git transport periodically as a LaunchAgent
* [dot git transport uninstall](dot_git_transport_uninstall.md)	 - Remove the periodic git transport check

