package cookiejar

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (j *Jar) GetAllCookies() (cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, hostCookies := range j.entries {
		for _, entry := range hostCookies {
			cookies = append(cookies, entry.c)
		}
	}
	return cookies
}

type PersistenceItem struct {
	// key 是在 cookieJar map 里面的 key
	Key     string
	DefPath string
	Host    string
	Cookie  *http.Cookie
	U       string
	// 这个时间是标识 session Cookie 被创建的时间，恢复的时候用来判断是否需要丢弃
	SessionCookieSetTime *time.Time `json:",omitempty"`
	// 改为使用导出时的时间
	SessionCookieExportTime *time.Time `json:",omitempty"`
	Domain                  string
}

type persistenceItemV1 struct {
	// key 是在 cookieJar map 里面的 key
	Key                  string
	DefPath              string
	Host                 string
	Cookie               *http.Cookie
	U                    url.URL
	SessionCookieSetTime *time.Time
	Domain               string
}

func v1ToNew(v1Items []persistenceItemV1) (ret []PersistenceItem) {
	for _, item := range v1Items {
		ret = append(ret, PersistenceItem{
			Key:                     item.Key,
			DefPath:                 item.DefPath,
			Host:                    item.Host,
			Cookie:                  item.Cookie,
			U:                       item.U.String(),
			SessionCookieSetTime:    item.SessionCookieSetTime,
			SessionCookieExportTime: item.SessionCookieSetTime,
			Domain:                  item.Domain,
		})
	}
	return
}

func (j *Jar) GetAllCookiesAsPersistenceItems() []PersistenceItem {
	j.mu.Lock()
	defer j.mu.Unlock()

	items := make([]PersistenceItem, 0)
	t := time.Now()
	for _, hostCookies := range j.entries {
		for _, e := range hostCookies {
			cookie := e.c
			// MaxAge 只在接受到 cookie的当时才有效，持久化的话要删除它
			if cookie.MaxAge > 0 {
				// prefer original Expires
				if cookie.Expires.IsZero() {
					cookie.Expires = e.Expires
				}
				cookie.MaxAge = 0
			}
			i := PersistenceItem{
				Key:     e.key,
				DefPath: e.defPath,
				Host:    e.host,
				Cookie:  cookie,
				U:       e.u.String(),
				Domain:  e.Domain,
			}
			if !e.Persistent {
				i.SessionCookieExportTime = &t
			}
			items = append(items, i)
		}
	}
	return items
}

func (j *Jar) SerializeCookiesToItems() []PersistenceItem {
	items := j.GetAllCookiesAsPersistenceItems()
	return items
}

func (j *Jar) SerializeCookiesToStr() (string, error) {
	items := j.SerializeCookiesToItems()
	if r, err := json.Marshal(items); err != nil {
		return "", err
	} else {
		return string(r), err
	}
}

func (j *Jar) DeserializeCookiesFromItemsWithDuration(items []PersistenceItem, sessionCookieAliveDuration time.Duration) (err error) {
	if len(items) == 0 {
		return
	}
	for _, i := range items {
		// 这里要用指针，否则所有cookie都会指向同一个地址
		cookie := i.Cookie
		if cookie.RawExpires != "" {
			cookie.Expires, err = ParseDateString(cookie.RawExpires)
			if err != nil {
				return err
			}
		}
		if !cookie.Expires.IsZero() && time.Now().Sub(cookie.Expires) > 0 {
			// delete expired cookies
			continue
		}
		// check the session cookie if expired
		// 改为使用导出时的时间 SessionCookieExportTime，这样只要 cookie 一直在被使用，就不会被丢弃
		if i.Cookie.MaxAge == 0 && i.Cookie.Expires.IsZero() && i.SessionCookieExportTime != nil {
			if !i.SessionCookieExportTime.IsZero() && sessionCookieAliveDuration > 0 {
				if time.Now().Sub(*i.SessionCookieExportTime) > sessionCookieAliveDuration {
					continue
				}
			}
		}
		u, err := url.Parse(i.U)
		if err != nil {
			return err
		}
		j.SetCookies(u, []*http.Cookie{
			cookie,
		})
	}
	return
}

func (j *Jar) DeserializeCookiesFromStr(cookiesStr string, sessionCookieAliveDuration time.Duration) (err error) {
	var items []PersistenceItem
	err = json.Unmarshal([]byte(cookiesStr), &items)
	if err != nil {
		itemsV1 := []persistenceItemV1{}
		err = json.Unmarshal([]byte(cookiesStr), &itemsV1)
		if err != nil {
			return err
		}
		items = v1ToNew(itemsV1)
	}
	return j.DeserializeCookiesFromItemsWithDuration(items, sessionCookieAliveDuration)
}

func SameSiteStrToInt(val string) (SameSite http.SameSite) {
	SameSite = http.SameSiteDefaultMode
	if len(val) == 0 {
		return
	}

	parts := strings.Split(val, "=")
	lowerVal := strings.ToLower(parts[1])
	switch lowerVal {
	case "lax":
		SameSite = http.SameSiteLaxMode
	case "strict":
		SameSite = http.SameSiteStrictMode
	case "none":
		SameSite = http.SameSiteNoneMode
	default:
		SameSite = http.SameSiteDefaultMode
	}
	return
}

func SameSiteIntToStr(sameSite http.SameSite) string {
	switch sameSite {
	case http.SameSiteLaxMode:
		return "lax"
	case http.SameSiteStrictMode:
		return "strict"
	case http.SameSiteNoneMode:
		return "none"
	default:
		return ""
	}
}

func ParseDateString(dt string) (t time.Time, err error) {
	layouts := []string{
		// rfc3389
		"2006-01-02T15:04:05Z07:00",
		// rfc1123
		"Mon, 02 Jan 2006 15:04:05 MST",
		"Mon, _2 Jan 2006 15:04:05 MST",

		"Mon, 02-Jan-2006 15:04:05 MST",
		"Mon, _2-Jan-2006 15:04:05 MST",

		"Mon, 02-Jan-06 15:04:05 MST",
		"Mon, _2-Jan-06 15:04:05 MST",
		// rfc 822
		"02 Jan 06 15:04 MST",
		// UnixDate
		"Mon Jan _2 15:04:05 MST 2006",
	}

	for _, layout := range layouts {
		if t, err = time.Parse(layout, dt); err == nil {
			return
		}
	}
	return
}
