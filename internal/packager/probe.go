package packager

import "context"

// SubtitleStreamInfo describes a single subtitle stream in a media file.
type SubtitleStreamInfo struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
	IsBitmap bool   `json:"isBitmap"`
	Forced   bool   `json:"forced"`
}

// ProbeSubtitleStreams runs ffprobe on path and returns all subtitle streams.
func ProbeSubtitleStreams(ctx context.Context, path string) ([]SubtitleStreamInfo, error) {
	probe, err := probeSource(ctx, path)
	if err != nil {
		return nil, err
	}
	var out []SubtitleStreamInfo
	for _, s := range probe.Streams {
		if s.CodecType != "subtitle" {
			continue
		}
		lang := s.Tags.Language
		if lang == "" {
			lang = "und"
		}
		out = append(out, SubtitleStreamInfo{
			Index:    s.Index,
			Codec:    s.CodecName,
			Language: lang,
			Title:    s.Tags.Title,
			IsBitmap: isBitmapSubtitle(s.CodecName),
			Forced:   s.Disposition.Forced == 1,
		})
	}
	return out, nil
}
