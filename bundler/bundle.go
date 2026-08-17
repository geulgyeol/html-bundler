package bundler

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/fxamacker/cbor/v2"
)

const (
	bundleMagicString   = "GLGYBNDL"
	metadataMagicString = "GLGYMETA"
	frameMagicString    = "GLGYFRAM"
	footerMagicString   = "GLGYFOOT"
)

var (
	bundleMagic   = [8]byte{'G', 'L', 'G', 'Y', 'B', 'N', 'D', 'L'}
	metadataMagic = [8]byte{'G', 'L', 'G', 'Y', 'M', 'E', 'T', 'A'}
	frameMagic    = [8]byte{'G', 'L', 'G', 'Y', 'F', 'R', 'A', 'M'}
	footerMagic   = [8]byte{'G', 'L', 'G', 'Y', 'F', 'O', 'O', 'T'}
)

func validateMagic(name string, got [8]byte, expected string) error {
	if string(got[:]) != expected {
		return fmt.Errorf("invalid %s magic: %s", name, string(got[:]))
	}

	return nil
}

type BundleHeader struct {
	Magic       [8]byte
	Version     uint16  // currently 3
	DictVersion uint16  // currently 2
	Reserved    [4]byte // reserved for future use, currently set to 0
}

type BundleMetadataHeader struct {
	Magic  [8]byte
	Type   uint32 // currently 0
	Length uint32 // length of the metadata content
}

type BundleMetadata struct {
	Header  BundleMetadataHeader
	Content []byte // CBOR-encoded metadata content
}

type BundleFrameHeader struct {
	Magic          [8]byte
	Type           uint32 // currently 0
	PayloadLength  uint32 // length of the payload content, must be metadata length + content length + 4 bytes for the checksum
	Timestamp      uint64 // timestamp of the frame, in milliseconds since epoch
	MetadataLength uint32 // length of the metadata content, must be 0 for now
	ContentLength  uint32 // length of the content, must be 0 for now
}

type BundleFramePayload struct {
	Metadata []byte // CBOR-encoded metadata content
	Content  []byte // compressed content
	Checksum uint32 // CRC32 checksum of the metadata and content
}

type BundleFrame struct {
	Header  BundleFrameHeader
	Payload BundleFramePayload
}

type BundleFooterHeader struct {
	Magic  [8]byte
	Type   uint32 // currently 0xFFFFFFFF
	Length uint32 // length of the footer content, must be 16 bytes for now
}

type BundleFooterPayload struct {
	FrameCount uint32   // number of frames in the bundle
	Reserved   [12]byte // reserved for future use, currently set to 0
}

type BundleFooter struct {
	Header  BundleFooterHeader
	Payload BundleFooterPayload
}

type Bundle struct {
	Header   BundleHeader
	Metadata BundleMetadata
	Frames   []BundleFrame
	Footer   BundleFooter
}

// read
func ReadBundleHeader(reader io.Reader) (BundleHeader, error) {
	var header BundleHeader
	err := binary.Read(reader, binary.LittleEndian, &header)
	if err != nil {
		return BundleHeader{}, err
	}
	return header, nil
}

func ReadBundleMetadataHeader(reader io.Reader) (BundleMetadataHeader, error) {
	var header BundleMetadataHeader
	err := binary.Read(reader, binary.LittleEndian, &header)
	if err != nil {
		return BundleMetadataHeader{}, err
	}
	return header, nil
}

func ReadBundleMetadata(reader io.Reader) (BundleMetadata, error) {
	var metadata BundleMetadata
	var err error
	metadata.Header, err = ReadBundleMetadataHeader(reader)
	if err != nil {
		return BundleMetadata{}, err
	}

	metadata.Content = make([]byte, metadata.Header.Length)
	_, err = io.ReadFull(reader, metadata.Content)
	if err != nil {
		return BundleMetadata{}, err
	}
	return metadata, nil
}

func ReadBundleFrameHeader(reader io.Reader) (BundleFrameHeader, error) {
	var header BundleFrameHeader
	err := binary.Read(reader, binary.LittleEndian, &header)
	if err != nil {
		return BundleFrameHeader{}, err
	}
	return header, nil
}

func ReadBundleFramePayload(reader io.Reader, metadataLength uint32, contentLength uint32) (BundleFramePayload, error) {
	var payload BundleFramePayload

	payload.Metadata = make([]byte, metadataLength)
	_, err := io.ReadFull(reader, payload.Metadata)
	if err != nil {
		return BundleFramePayload{}, err
	}

	payload.Content = make([]byte, contentLength)
	_, err = io.ReadFull(reader, payload.Content)
	if err != nil {
		return BundleFramePayload{}, err
	}

	err = binary.Read(reader, binary.LittleEndian, &payload.Checksum)
	if err != nil {
		return BundleFramePayload{}, err
	}

	return payload, nil
}

func ReadBundleFrame(reader io.Reader) (BundleFrame, error) {
	var frame BundleFrame
	var err error

	frame.Header, err = ReadBundleFrameHeader(reader)
	if err != nil {
		return BundleFrame{}, err
	}

	frame.Payload, err = ReadBundleFramePayload(reader, frame.Header.MetadataLength, frame.Header.ContentLength)
	if err != nil {
		return BundleFrame{}, err
	}

	return frame, nil
}

func ReadBundleFooterHeader(reader io.Reader) (BundleFooterHeader, error) {
	var header BundleFooterHeader
	err := binary.Read(reader, binary.LittleEndian, &header)
	if err != nil {
		return BundleFooterHeader{}, err
	}
	return header, nil
}

func ReadBundleFooterPayload(reader io.Reader) (BundleFooterPayload, error) {
	var payload BundleFooterPayload
	err := binary.Read(reader, binary.LittleEndian, &payload)
	if err != nil {
		return BundleFooterPayload{}, err
	}
	return payload, nil
}

func ReadBundleFooter(reader io.Reader) (BundleFooter, error) {
	var footer BundleFooter
	var err error

	footer.Header, err = ReadBundleFooterHeader(reader)
	if err != nil {
		return BundleFooter{}, err
	}

	footer.Payload, err = ReadBundleFooterPayload(reader)
	if err != nil {
		return BundleFooter{}, err
	}

	return footer, nil
}

func ReadBundle(reader io.Reader, validate bool) (Bundle, error) {
	var bundle Bundle
	var err error
	bufferedReader := bufio.NewReader(reader)

	bundle.Header, err = ReadBundleHeader(bufferedReader)
	if err != nil {
		return Bundle{}, err
	}

	if validate {
		if err := bundle.Header.Validate(); err != nil {
			return Bundle{}, err
		}
	}

	bundle.Metadata, err = ReadBundleMetadata(bufferedReader)
	if err != nil {
		return Bundle{}, err
	}

	if validate {
		if err := bundle.Metadata.Header.Validate(); err != nil {
			return Bundle{}, err
		}
	}

	for {
		nextMagic, err := bufferedReader.Peek(8)
		if err != nil {
			if err == io.EOF {
				return Bundle{}, fmt.Errorf("bundle footer not found")
			}
			return Bundle{}, err
		}

		switch string(nextMagic) {
		case frameMagicString:
			frame, err := ReadBundleFrame(bufferedReader)
			if err != nil {
				return Bundle{}, err
			}

			if validate {
				if err := frame.Validate(); err != nil {
					return Bundle{}, err
				}
			}

			bundle.Frames = append(bundle.Frames, frame)
		case footerMagicString:
			bundle.Footer, err = ReadBundleFooter(bufferedReader)
			if err != nil {
				return Bundle{}, err
			}

			if validate {
				if err := bundle.Footer.Validate(); err != nil {
					return Bundle{}, err
				}
			}

			goto done
		default:
			return Bundle{}, fmt.Errorf("unknown section magic: %q", string(nextMagic))
		}
	}

done:

	if bundle.Footer.Header.Magic == [8]byte{} {
		return Bundle{}, fmt.Errorf("bundle footer not found")
	}

	if validate {
		if err := bundle.Header.Validate(); err != nil {
			return Bundle{}, err
		}
		if err := bundle.Metadata.Header.Validate(); err != nil {
			return Bundle{}, err
		}
	}

	if len(bundle.Frames) != int(bundle.Footer.Payload.FrameCount) {
		return Bundle{}, fmt.Errorf("frame count mismatch: header=%d, actual=%d", bundle.Footer.Payload.FrameCount, len(bundle.Frames))
	}

	return bundle, nil
}

func (header *BundleHeader) Validate() error {
	if err := validateMagic("bundle", header.Magic, bundleMagicString); err != nil {
		return err
	}

	if header.Version != 3 {
		return fmt.Errorf("unsupported bundle version: %d", header.Version)
	}

	if header.DictVersion != 0 && header.DictVersion != 2 {
		return fmt.Errorf("unsupported dictionary version: %d", header.DictVersion)
	}

	return nil
}

func (metadataHeader *BundleMetadataHeader) Validate() error {
	if err := validateMagic("metadata", metadataHeader.Magic, metadataMagicString); err != nil {
		return err
	}

	if metadataHeader.Type != 0 {
		return fmt.Errorf("unsupported metadata type: %d", metadataHeader.Type)
	}

	return nil
}

func (frameHeader *BundleFrameHeader) Validate() error {
	if err := validateMagic("frame", frameHeader.Magic, frameMagicString); err != nil {
		return err
	}

	if frameHeader.Type != 0 {
		return fmt.Errorf("unsupported frame type: %d", frameHeader.Type)
	}

	if frameHeader.PayloadLength != frameHeader.MetadataLength+frameHeader.ContentLength+4 {
		return fmt.Errorf("payload length mismatch: header=%d, actual=%d", frameHeader.PayloadLength, frameHeader.MetadataLength+frameHeader.ContentLength+4)
	}

	return nil
}

func (framePayload *BundleFramePayload) Validate() error {
	checksum := crc32.Checksum(append(framePayload.Metadata, framePayload.Content...), crc32.MakeTable(crc32.Castagnoli))
	if checksum != framePayload.Checksum {
		return fmt.Errorf("checksum mismatch: expected=%d, actual=%d", framePayload.Checksum, checksum)
	}

	return nil
}

func (frame *BundleFrame) Validate() error {
	if err := frame.Header.Validate(); err != nil {
		return err
	}

	if err := frame.Payload.Validate(); err != nil {
		return err
	}

	return nil
}

func (footerHeader *BundleFooterHeader) Validate() error {
	if err := validateMagic("footer", footerHeader.Magic, footerMagicString); err != nil {
		return err
	}

	if footerHeader.Type != 0xFFFFFFFF {
		return fmt.Errorf("unsupported footer type: %d", footerHeader.Type)
	}

	if footerHeader.Length != 16 {
		return fmt.Errorf("unsupported footer length: %d", footerHeader.Length)
	}

	return nil
}

func (footer *BundleFooter) Validate() error {
	if err := footer.Header.Validate(); err != nil {
		return err
	}

	return nil
}

func (bundle *Bundle) Validate() error {
	if err := bundle.Header.Validate(); err != nil {
		return err
	}

	if err := bundle.Metadata.Header.Validate(); err != nil {
		return err
	}

	for _, frame := range bundle.Frames {
		if err := frame.Validate(); err != nil {
			return err
		}
	}

	if err := bundle.Footer.Validate(); err != nil {
		return err
	}

	if len(bundle.Frames) != int(bundle.Footer.Payload.FrameCount) {
		return fmt.Errorf("frame count mismatch: header=%d, actual=%d", bundle.Footer.Payload.FrameCount, len(bundle.Frames))
	}

	return nil
}

// write
func WriteBundleHeader(writer io.Writer, header BundleHeader) error {
	err := binary.Write(writer, binary.LittleEndian, header)
	if err != nil {
		return err
	}
	return nil
}

func WriteBundleMetadataHeader(writer io.Writer, header BundleMetadataHeader) error {
	err := binary.Write(writer, binary.LittleEndian, header)
	if err != nil {
		return err
	}
	return nil
}

func WriteBundleMetadata(writer io.Writer, metadata BundleMetadata) error {
	err := WriteBundleMetadataHeader(writer, metadata.Header)
	if err != nil {
		return err
	}

	_, err = writer.Write(metadata.Content)
	if err != nil {
		return err
	}
	return nil
}

func WriteBundleFrameHeader(writer io.Writer, header BundleFrameHeader) error {
	err := binary.Write(writer, binary.LittleEndian, header)
	if err != nil {
		return err
	}
	return nil
}

func WriteBundleFramePayload(writer io.Writer, payload BundleFramePayload) error {
	_, err := writer.Write(payload.Metadata)
	if err != nil {
		return err
	}

	_, err = writer.Write(payload.Content)
	if err != nil {
		return err
	}

	err = binary.Write(writer, binary.LittleEndian, payload.Checksum)
	if err != nil {
		return err
	}
	return nil
}

func WriteBundleFrame(writer io.Writer, frame BundleFrame) error {
	err := WriteBundleFrameHeader(writer, frame.Header)
	if err != nil {
		return err
	}

	err = WriteBundleFramePayload(writer, frame.Payload)
	if err != nil {
		return err
	}
	return nil
}

func WriteBundleFooterHeader(writer io.Writer, header BundleFooterHeader) error {
	err := binary.Write(writer, binary.LittleEndian, header)
	if err != nil {
		return err
	}
	return nil
}

func WriteBundleFooterPayload(writer io.Writer, payload BundleFooterPayload) error {
	err := binary.Write(writer, binary.LittleEndian, payload)
	if err != nil {
		return err
	}
	return nil
}

func WriteBundleFooter(writer io.Writer, footer BundleFooter) error {
	err := WriteBundleFooterHeader(writer, footer.Header)
	if err != nil {
		return err
	}

	err = WriteBundleFooterPayload(writer, footer.Payload)
	if err != nil {
		return err
	}
	return nil
}

func WriteBundle(writer io.Writer, bundle Bundle) error {
	err := WriteBundleHeader(writer, bundle.Header)
	if err != nil {
		return err
	}

	err = WriteBundleMetadata(writer, bundle.Metadata)
	if err != nil {
		return err
	}

	for _, frame := range bundle.Frames {
		err = WriteBundleFrame(writer, frame)
		if err != nil {
			return err
		}
	}

	err = WriteBundleFooter(writer, bundle.Footer)
	if err != nil {
		return err
	}

	return nil
}

// streaming write wrapper

type BundleWriter struct {
	writer   *io.WriteCloser
	IsClosed bool
	Length   uint64
	Count    int
}

func NewBundle(writer_ io.WriteCloser, metadata any) (*BundleWriter, error) {
	writer := BundleWriter{
		writer:   &writer_,
		IsClosed: false,
		Length:   0,
		Count:    0,
	}

	// write the header
	header := BundleHeader{
		Magic:       bundleMagic,
		Version:     3,
		DictVersion: 2,
	}

	err := WriteBundleHeader(*writer.writer, header)
	if err != nil {
		return nil, fmt.Errorf("Failed to write bundle header: %v", err)
	}
	writer.Length += 16 // header is always 16 bytes

	// write the metadata content
	metadataBytes, err := cbor.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("Failed to marshal bundle metadata: %v", err)
	}

	metadataHeader := BundleMetadataHeader{
		Magic:  metadataMagic,
		Type:   0,
		Length: uint32(len(metadataBytes)),
	}

	err = WriteBundleMetadataHeader(*writer.writer, metadataHeader)
	if err != nil {
		return nil, fmt.Errorf("Failed to write bundle metadata header: %v", err)
	}
	writer.Length += 16 // metadata header is always 16 bytes

	_, err = (*writer.writer).Write(metadataBytes)
	if err != nil {
		return nil, fmt.Errorf("Failed to write bundle metadata content: %v", err)
	}
	writer.Length += uint64(len(metadataBytes))

	return &writer, nil
}

func (bw *BundleWriter) WriteFrameRaw(frame BundleFrame) error {
	if bw.IsClosed {
		return fmt.Errorf("bundle writer is closed")
	}

	err := WriteBundleFrame(*bw.writer, frame)
	if err != nil {
		return err
	}
	bw.Length += uint64(frame.Header.PayloadLength) + 32 // frame header is always 32 bytes
	bw.Count++

	return nil
}

func (bw *BundleWriter) WriteFrame(timestamp uint64, metadata any, content []byte) error {
	metadataBytes, err := cbor.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("Failed to marshal frame metadata: %v", err)
	}

	frame := BundleFrame{
		Header: BundleFrameHeader{
			Magic:          frameMagic,
			Type:           0,
			PayloadLength:  uint32(len(metadataBytes) + len(content) + 4),
			MetadataLength: uint32(len(metadataBytes)),
			ContentLength:  uint32(len(content)),
			Timestamp:      timestamp,
		},
		Payload: BundleFramePayload{
			Metadata: metadataBytes,
			Content:  content,
			Checksum: crc32.Checksum(append(metadataBytes, content...), crc32.MakeTable(crc32.Castagnoli)),
		},
	}

	return bw.WriteFrameRaw(frame)
}

func (bw *BundleWriter) Close() error {
	if bw.IsClosed {
		return fmt.Errorf("bundle writer is already closed")
	}

	footer := BundleFooter{
		Header: BundleFooterHeader{
			Magic:  footerMagic,
			Type:   0xFFFFFFFF,
			Length: 16,
		},
		Payload: BundleFooterPayload{
			FrameCount: uint32(bw.Count),
			Reserved:   [12]byte{},
		},
	}

	err := WriteBundleFooter(*bw.writer, footer)
	if err != nil {
		return err
	}
	bw.Length += 16 + 16 // footer header is always 16 bytes, footer payload is always 16 bytes (for now)
	bw.IsClosed = true

	return (*bw.writer).Close()
}
