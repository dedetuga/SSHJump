# Used only by the official Axis SDK build path (Dockerfile, `acap-build`).
# The Go binary is produced by the Go build stage and copied in already named
# `sshjump` (== manifest appName), so acap-build just needs to package it.
# This target is intentionally a no-op.
PROG = sshjump

all:
	@echo "sshjump binary is prebuilt by the Go stage; nothing to compile here."

clean:
	@true
