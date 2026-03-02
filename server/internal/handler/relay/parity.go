package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	defaultVideoFrames = 121
	longVideoFrames    = 241
	shortVideoSeconds  = 5
	longVideoSeconds   = 10
)

var videoReqKeySet = map[string]struct{}{
	"jimeng_video_v30":               {},
	"jimeng_t2v_v30":                 {},
	"jimeng_t2v_v30_1080p":           {},
	"jimeng_ti2v_v30_pro":            {},
	"jimeng_i2v_first_v30":           {},
	"jimeng_i2v_first_tail_v30":      {},
	"jimeng_i2v_first_v30_1080":      {},
	"jimeng_i2v_first_tail_v30_1080": {},
	"jimeng_i2v_recamera_v30":        {},
}

func ValidateVideoFrames(frames int) error {
	if frames == defaultVideoFrames || frames == longVideoFrames {
		return nil
	}
	return fmt.Errorf("invalid frames: must be 121 or 241")
}

func NormalizeVideoFrames(frames *int) int {
	if frames == nil {
		return defaultVideoFrames
	}
	return *frames
}

func FramesToDuration(frames int) (int, error) {
	switch frames {
	case defaultVideoFrames:
		return shortVideoSeconds, nil
	case longVideoFrames:
		return longVideoSeconds, nil
	default:
		return 0, fmt.Errorf("invalid frames: must be 121 or 241")
	}
}

func normalizeSubmitVideoFrames(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	var payload map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return body, nil
	}

	if !isVideoSubmitPayload(payload) {
		return body, nil
	}

	frames, provided, err := readFrames(payload["frames"])
	if err != nil {
		return nil, err
	}

	var ptr *int
	if provided {
		ptr = &frames
	}
	effectiveFrames := NormalizeVideoFrames(ptr)
	if err := ValidateVideoFrames(effectiveFrames); err != nil {
		return nil, err
	}

	payload["frames"] = effectiveFrames
	normalizedBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("normalize video frames payload: %w", err)
	}

	return normalizedBody, nil
}

func isVideoSubmitPayload(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	reqKey, _ := payload["req_key"].(string)
	reqKey = strings.TrimSpace(reqKey)
	_, ok := videoReqKeySet[reqKey]
	return ok
}

func readFrames(raw any) (int, bool, error) {
	if raw == nil {
		return 0, false, nil
	}

	switch v := raw.(type) {
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false, fmt.Errorf("invalid frames: must be integer 121 or 241")
		}
		return int(n), true, nil
	case float64:
		n := int(v)
		if float64(n) != v {
			return 0, false, fmt.Errorf("invalid frames: must be integer 121 or 241")
		}
		return n, true, nil
	case int:
		return v, true, nil
	case int32:
		return int(v), true, nil
	case int64:
		return int(v), true, nil
	default:
		return 0, false, fmt.Errorf("invalid frames: must be integer 121 or 241")
	}
}
