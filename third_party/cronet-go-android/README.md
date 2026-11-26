# Cronet Android cgo compatibility patch

These tiny modules replace the upstream `cronet-go/lib/android_*` modules only
for the linker directive that Go 1.26.11 rejects:

- upstream: `-l:libcronet.a`
- patched: `-lcronet`

Both resolve the same pinned `libcronet.a` through `-L${SRCDIR}`. The static
libraries are not committed. `cmd/internal/build_libbox` copies them from the
pinned upstream modules before an Android bind and removes them afterwards.
