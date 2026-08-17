## html-bundler

Bundles crawled HTML files into a single efficient bundle, and upload them to S3-compatible object storage.

### Features

- Replaces `html-storage` and `html-precompressor` by working as a sidecar
  - Each node directly writes to object storage, reducing network traffic and storage burden
- Natively built for object storage without providing read access
- Highly efficient compression of HTML files using zstd w. dictionary

## Bundle spec

A **bundle** is a single file containing **frames** of HTML files and metadata. You can find the reference implementation in `bundle.go`. This format is suitable for object storage, streaming read/write, and efficient compression. You can inspect a bundle using `go run ./cmd/inspect -file [path] --dump-metadata --dump-html`.

- A *timestamp* is a 64-bit unsigned integer of UNIX time in milliseconds.
- All integers must be in little-endian format.
- Reserved fields must be set to 0 for now, and ignored by the reader.

### Bundle header (16 bytes)
- Magic number: `GLGYBNDL` (8 bytes)
- Version: uint16 (2 bytes, currently 3)
- Dictionary version: uint16 (2 bytes, currently 2 for `zstd_dict_v2`. 0 for no dictionary.)
- Reserved: 4 bytes

### Bundle metadata (variable length)

**Bundle metadata header (16 bytes)**
- Magic number: `GLGYMETA` (8 bytes)
- Type: uint32 (4 bytes, currently always 0)
- Length: uint32 (4 bytes)

**Bundle metadata content (variable length)**
- Exactly one CBOR map containing metadata about the bundle, at least including but not limited to:
  - `timestamp`: timestamp of the bundle creation (start of writing)

### Bundle frames

**Frame header (32 bytes)**
- Magic number: `GLGYFRAM` (8 bytes)
- Type: uint32 (4 bytes, currently always 0)
- Payload Length: uint32 (4 bytes, metadata length + content length + 4 bytes for checksum)
- Timestamp: uint64 (8 bytes)
- Metadata length: uint32 (4 bytes)
- Content length: uint32 (4 bytes)

The implementation should verify that the payload length equals metadata length + content length + 4 bytes for checksum.

**Frame payload (variable length)**
- Metadata: Exactly one CBOR map containing metadata about the frame, at least including but not limited to:
  - `id`: ID of the HTML file
- Content: zstd-compressed HTML file content
- Frame checksum: CRC32C of the metadata and content (4 bytes)

### Bundle footer header (16 bytes)
- Magic number: `GLGYFOOT` (8 bytes)
- Type: uint32 (4 bytes, currently always 0xFFFFFFFF)
- Length: uint32 (4 bytes, currently always 16)

### Bundle footer payload (16 bytes)

- Number of frames: uint32 (4 bytes)
- Reserved: 12 bytes

### License

MIT License