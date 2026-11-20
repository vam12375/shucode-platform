package media

import (
	"crypto/hmac"
	"net/url"
	"strconv"
	"time"
)

const DisplayURLTTL = 5 * time.Minute

func displayExpires(now time.Time) string {
	return strconv.FormatInt(now.Truncate(time.Minute).Add(DisplayURLTTL).Unix(), 10)
}

// 用户展示授权按短时间桶复用；供应商 URL 保留原来的长有效期。
func (b Blobs) DisplayURL(team, id string) string {
	expires := displayExpires(time.Now())
	params := url.Values{"expires": {expires}, "signature": {b.signature(team, id, expires)}}
	return b.Origin + "/api/v1/media/blobs/" + team + "/" + id + "?" + params.Encode()
}

// 团队附件签名与普通媒体域分离，并绑定变体和不可变内容版本。
func (b Blobs) DisplayPathURL(path, version string) string {
	expires := displayExpires(time.Now())
	params := url.Values{"expires": {expires}, "signature": {b.signature("display:"+path, version, expires)}, "v": {version}}
	return b.Origin + path + "?" + params.Encode()
}

func (b Blobs) VerifyDisplayPath(path, version, expires, signature string) bool {
	now := time.Now().Unix()
	value, err := strconv.ParseInt(expires, 10, 64)
	return err == nil && value > now && value <= now+int64(DisplayURLTTL/time.Second) &&
		signature != "" && hmac.Equal([]byte(signature), []byte(b.signature("display:"+path, version, expires)))
}
