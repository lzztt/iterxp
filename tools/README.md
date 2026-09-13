# tools

Source code for IterXP self-developed tools.

## Layout

Each tool should be a Go command in its own directory:

```text
tools/
  my-tool/
    main.go
```

## Install compiled tools

Run from the repository root:

```sh
./tools/install.sh
```

Compiled tools are written to `$ITERXP_CONFIG_DIR/tools`, or by default
`~/.iterxp_v2/tools/`.
