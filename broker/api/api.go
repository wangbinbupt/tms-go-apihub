package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	klog "k8s.io/klog/v2"

	"github.com/jasony62/tms-go-apihub/hub"
	"github.com/jasony62/tms-go-apihub/unit"
	"github.com/jasony62/tms-go-apihub/util"
)

// 转发API调用
func Run(stack *hub.Stack) (interface{}, int) {
	var err error
	apiDef, err := unit.FindApiDef(stack, stack.Name)

	if apiDef == nil {
		klog.Errorln("获得API定义失败：", err)
		panic(err)
	}

	var jsonInRspBody interface{}
	var jsonOutRspBody interface{}

	if apiDef.Cache != nil { //如果Json文件中配置了cache，表示支持缓存
		if content := GetCacheContentWithLock(apiDef); content == nil {
			defer apiDef.Cache.Locker.Unlock()
			apiDef.Cache.Locker.Lock()

			if content = GetCacheContent(apiDef); content == nil {
				klog.Infoln("获取缓存Cache ... ...")
				outReq := NewRequest(stack, apiDef)
				// 发出请求
				client := &http.Client{}
				resp, err := client.Do(outReq)
				if err != nil {
					klog.Errorln("err", err)
					return nil, 500
				}
				defer resp.Body.Close()
				returnBody, _ := io.ReadAll(resp.Body)

				// 将收到的结果转为JSON对象
				json.Unmarshal(returnBody, &jsonInRspBody)

				//解析过期时间，如果存在则记录下来
				//str := `{"msg":"鉴权成功","expireTime":"20220510153521","ak":"MTY1MjEMTAwMU1UWTFNakUyT0RFeU1UUTNNeU14TURBd01USTJNQT09","resultcode":"1"}`
				//expires, ok := HandleExpireTime(stack, resp, str, apiDef)
				expires, ok := HandleExpireTime(stack, resp, string(returnBody), apiDef)
				if !ok {
					klog.Warningln("没有查询到过期时间")
					// 构造发送的响应内容
					jsonOutRspBody = NewOutRspBody(apiDef, jsonInRspBody)
				} else {
					klog.Infof("更新Cache信息，过期时间为: %v", expires)
					apiDef.Cache.Expires = expires
					jsonOutRspBody = NewOutRspBody(apiDef, jsonInRspBody)
					apiDef.Cache.Resp = jsonOutRspBody
				}
			} else {
				klog.Infoln("Cache缓存有效，直接回应")
				jsonOutRspBody = content
			}
		} else {
			klog.Infoln("Cache缓存有效，直接回应")
			jsonOutRspBody = content
		}
	} else { //不支持缓存，直接请求
		klog.Infoln("不支持Cache缓存 ... ...")
		outReq := NewRequest(stack, apiDef)
		// 发出请求
		client := &http.Client{}
		resp, err := client.Do(outReq)
		if err != nil {
			klog.Errorln("err", err)
			return nil, 500
		}
		defer resp.Body.Close()
		returnBody, _ := io.ReadAll(resp.Body)
		// 将收到的结果转为JSON对象
		json.Unmarshal(returnBody, &jsonInRspBody)

		jsonOutRspBody = NewOutRspBody(apiDef, jsonInRspBody)
	}

	klog.Infoln("处理", apiDef.Url, ":", http.StatusOK, "\r\n返回结果：", jsonOutRspBody)
	return jsonOutRspBody, http.StatusOK
}

// 构造发送的响应内容
func NewOutRspBody(apiDef *hub.ApiDef, in interface{}) interface{} {
	var out interface{}
	if apiDef.Response != nil && apiDef.Response.Json != nil {
		out = util.Json2Json(in, apiDef.Response.Json)
	} else {
		// 直接转发返回的结果
		out = in
	}
	return out
}

func NewRequest(stack *hub.Stack, apiDef *hub.ApiDef) *http.Request {
	var formBody *http.Request
	var outBody string
	var hasBody bool
	// 要发送的请求
	outReq, _ := http.NewRequest(apiDef.Method, "", nil)
	hasBody = len(apiDef.RequestContentType) > 0 && apiDef.RequestContentType != "none"
	if hasBody {
		switch apiDef.RequestContentType {
		case "form":
			outReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			formBody = new(http.Request)
			formBody.ParseForm()
		case "json":
			outReq.Header.Set("Content-Type", "application/json")
		case hub.OriginName:
			contentType := stack.GinContext.Request.Header.Get("Content-Type")
			outReq.Header.Set("Content-Type", contentType)
			// 收到的请求中的数据
			inData, _ := json.Marshal(stack.StepResult[hub.OriginName])
			outBody = string(inData)
		default:
			outReq.Header.Set("Content-Type", apiDef.RequestContentType)
		}
	}

	// 发出请求的URL
	outReqURL, _ := url.Parse(apiDef.Url)
	// 设置请求参数
	outReqParamRules := apiDef.Parameters
	if outReqParamRules != nil {
		paramLen := len(*outReqParamRules)
		if paramLen > 0 {
			var value string
			q := outReqURL.Query()
			vars := make(map[string]string, paramLen)
			stack.StepResult[hub.VarsName] = vars
			defer func() { stack.StepResult[hub.VarsName] = nil }()

			for _, param := range *outReqParamRules {
				if len(param.Name) > 0 {
					if len(param.Value) == 0 {
						if param.From != nil {
							value = unit.GetParameterValue(stack, apiDef.Privates, param.From)
						}
					} else {
						value = param.Value
					}

					switch param.In {
					case "query":
						q.Set(param.Name, value)
					case "header":
						outReq.Header.Set(param.Name, value)
					case "body":
						if hasBody && apiDef.RequestContentType != hub.OriginName {
							if apiDef.RequestContentType == "form" {
								formBody.Form.Add(param.Name, value)
							} else {
								if len(outBody) == 0 {
									outBody = value
								} else {
									klog.Infoln("Double content body :\r\n", outBody, "\r\nVS\r\n", value)
								}
							}
						} else {
							klog.Infoln("Refuse to set body :", apiDef.RequestContentType, "VS\r\n", value)
						}
					case hub.VarsName:
					default:
						klog.Infoln("Invalid in:", param.In, "名字", param.Name, "值", value)
					}
					vars[param.Name] = value
					klog.Infoln("设置入参，位置", param.In, "名字", param.Name, "值", value)
				}
			}
			outReqURL.RawQuery = q.Encode()
		}
	}

	outReq.URL = outReqURL

	// 处理要发送的消息体
	if apiDef.Method == "POST" {
		if apiDef.RequestContentType != "none" {
			if apiDef.RequestContentType == "form" {
				outBody = formBody.Form.Encode()
			}
			outReq.Body = ioutil.NopCloser(strings.NewReader(outBody))
		}
	}

	return outReq
}

func HandleExpireTime(stack *hub.Stack, resp *http.Response, body string, apiDef *hub.ApiDef) (time.Time, bool) {
	//首先在api 的json文件中配置参数 cache
	// "cache": {
	// 	"from": {
	// 		"from": "header",
	// 		"name": "Set-Cookie.expires"
	// 	},
	// 	"format": "Mon, 02-Jan-06 15:04:05 MST"
	//   }
	//from 为从header还是从body中获取过期时间
	//name 为获取过期时间的关键字串
	//format：如果是date格式，则配置具体格式串，如果是second数，则按照秒数解析
	//	baidu_image_classify_token: Mon, 02-Jan-06 15:04:05 MST
	//	body中一个例子："expireTime":"20220510153521",格式为：20060102150405

	var src, key, format string
	src = apiDef.Cache.From.From
	key = apiDef.Cache.From.Name
	format = apiDef.Cache.Format
	klog.Infoln("获得参数，[src]:", src, "; [key]:", key, "; [format]:", format)

	if src == "" || key == "" || format == "" {
		klog.Warningln("Json文件中未配置过期时间参数")
		return time.Time{}, false
	}

	if strings.EqualFold(src, "header") {
		if strings.Contains(key, "Set-Cookie.") {
			key = strings.TrimPrefix(key, "Set-Cookie.")
			//判断Set-Cookie中是否含有Expires 的header
			cookie := resp.Header.Get("Set-Cookie")
			klog.Infoln("Header中Set-Cookie: ", cookie)
			if len(cookie) > 0 {
				expiresIndex := strings.Index(cookie, key) //"expires="
				if expiresIndex >= 0 {
					semicolonIndex := strings.Index(cookie[expiresIndex:], ";")
					if semicolonIndex < 0 {
						semicolonIndex = 0
					}
					expiresStr := cookie[expiresIndex+len(key)+1 : expiresIndex+semicolonIndex]

					expires, err := ParseExpireTime(expiresStr, format)
					if err == nil {
						return expires, true
					}
				}
			}
		} else {
			//判断是否含有Expires 的header
			expireHeader := resp.Header.Get(key)
			klog.Infoln("Header中Expires: ", expireHeader)
			if len(expireHeader) > 0 {
				expires, err := ParseExpireTime(expireHeader, format)
				if err == nil {
					return expires, true
				}
			}
		}
	} else if strings.EqualFold(src, "body") {
		//例"expireTime":"20220510153521",
		klog.Infoln("消息体:", body)
		index := strings.Index(body, key)
		if index >= 0 {
			colon := strings.Index(body[index:], ":")
			str := body[index+colon+1:]
			str = strings.TrimSpace(str)

			//如果是过期时间是秒的话，对象为整数
			if strings.EqualFold(format, "second") {
				if str[0] >= '0' && str[0] <= '9' { //如果过期时间key对应的值是数字
					reg := regexp.MustCompile(`[0-9]+`)
					strarray := reg.FindAllString(str, -1)
					str = strarray[0]
				}
			} else {
				//如果是过期时间是日期格式的话，对象为字符串，有 " 号
				if str[0] == '"' {
					str = strings.TrimLeft(str, `"`)
					quotesEnd := strings.Index(str, `"`)
					if quotesEnd >= 0 {
						str = str[:quotesEnd]
					}
				}
			}
			klog.Infoln("消息体中过期时间:", str)
			formatTime, err := ParseExpireTime(str, format)
			if err == nil {
				return formatTime, true
			}
		}
	} else {
		klog.Warningln("Json文件中未配置过期时间的来源: ", src)
	}

	return time.Time{}, false
}

func ParseExpireTime(str string, format string) (time.Time, error) {
	var exptime time.Time
	var err error

	if format == "second" {
		var s int
		s, err = strconv.Atoi(str)
		if err != nil {
			klog.Errorln("解析过期时间失败, err: ", err)
			return time.Time{}, errors.New("Parse expires failed")
		}

		exptime = time.Now()
		exptime = exptime.Add(time.Second * time.Duration(s))
	} else {
		exptime, err = time.Parse(format, str)
		if err != nil {
			klog.Errorln("解析过期时间失败, err: ", err)
			return time.Time{}, errors.New("Parse expires failed")
		}
	}
	klog.Infoln("解析后过期时间: ", exptime)
	return exptime.Local(), nil
}

func GetCacheContent(apiDef *hub.ApiDef) interface{} {
	//如果支持缓存，判断过期时间
	if time.Now().Local().After(apiDef.Cache.Expires) {
		return nil
	}
	return apiDef.Cache.Resp
}

func GetCacheContentWithLock(apiDef *hub.ApiDef) interface{} {
	//如果支持缓存，判断过期时间
	apiDef.Cache.Locker.RLock()
	defer apiDef.Cache.Locker.RUnlock()
	if time.Now().Local().After(apiDef.Cache.Expires) {
		return nil
	}
	return apiDef.Cache.Resp
}
