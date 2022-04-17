package api

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	"github.com/jasony62/tms-go-apihub/hub"
	"github.com/jasony62/tms-go-apihub/unit"
	"github.com/jasony62/tms-go-apihub/util"
)

// 转发API调用
func Relay(stack *hub.Stack, resultKey string) (interface{}, int) {
	apiDef := stack.ApiDef

	// 要发送的请求
	outReq, _ := http.NewRequest(apiDef.Method, "", nil)

	// 发出请求的URL
	outReqURL, _ := url.Parse(apiDef.Url)

	// 设置请求参数
	outReqParamRules := apiDef.Parameters
	if outReqParamRules != nil && len(*outReqParamRules) > 0 {
		q := outReqURL.Query()
		for _, param := range *outReqParamRules {
			if len(param.Name) > 0 {
				var value string

				if len(param.Value) > 0 {
					// 定义中指定的值
					value = param.Value
				} else if param.From != nil && len(param.From.Name) > 0 {
					if param.From.In == "query" {
						// 从调用上下文中获取参数值
						value = stack.Query(param.From.Name)
					} else if param.From.In == "StepResult" {
						// 从调用上下文中获取参数值
						value = stack.QueryFromStepResult(param.From.Name)
					} else if param.From.In == "private" {
						// 从私有数据中获取参数值
						value = unit.FindPrivateValue(stack.ApiDef, param.From.Name)
					}
				}

				if param.In == "query" {
					q.Set(param.Name, value)
				} else if param.In == "header" {
					outReq.Header.Set(param.Name, value)
				}
			}
		}
		outReqURL.RawQuery = q.Encode()
	}
	outReq.URL = outReqURL

	// 处理要发送的消息体
	if apiDef.Method == "POST" {
		if apiDef.RequestBody != nil {
			// 指定了发送内容的映射规则
			contentType := apiDef.RequestBody.ContentType
			if len(contentType) > 0 {
				outReq.Header.Set("Content-Type", contentType)
			} else {
				outReq.Header.Set("Content-Type", "application/json")
			}
			var outBody string
			if apiDef.RequestBody.Content != nil {
				// 收到的请求中的数据
				inData := stack.RequestBody
				// 根据映射规则生成数据
				jsonOutBody := util.Json2Json(inData, apiDef.RequestBody.Content)
				// 要求输出的表单形式数据
				if contentType == "application/x-www-form-urlencoded" {
					formData, _ := url.ParseQuery(jsonOutBody.(string))
					outBody = formData.Encode()
				} else { // 默认用JSON发送数据
					byteJson, _ := json.Marshal(jsonOutBody)
					outBody = string(byteJson)
				}
			}
			outReqBody := ioutil.NopCloser(strings.NewReader(outBody))
			outReq.Body = outReqBody
		} else {
			// 直接转发收到的数据
			contentType := stack.GinContext.Request.Header.Get("Content-Type")
			outReq.Header.Set("Content-Type", contentType)
			// 收到的请求中的数据
			inData, _ := json.Marshal(stack.RequestBody)
			outReqBody := ioutil.NopCloser(strings.NewReader(string(inData)))
			outReq.Body = outReqBody
		}
	}

	// 发出请求
	client := &http.Client{}
	resp, err := client.Do(outReq)
	if err != nil {
		fmt.Println("err", err)
		return nil, 500
	}
	defer resp.Body.Close()
	returnBody, _ := io.ReadAll(resp.Body)

	// 将收到的结果转为JSON对象
	var jsonInRspBody interface{}
	json.Unmarshal(returnBody, &jsonInRspBody)

	// 构造发送的响应内容
	if apiDef.Response != nil && apiDef.Response.Json != nil {
		outRspBodyRules := apiDef.Response.Json
		jsonOutRspBody := util.Json2Json(jsonInRspBody, outRspBodyRules)
		// 在上下文中保存结果
		if len(resultKey) > 0 {
			stack.StepResult[resultKey] = jsonOutRspBody
		}
		return jsonOutRspBody, http.StatusOK
	} else {
		// 直接转发返回的结果
		// 在上下文中保存结果
		if len(resultKey) > 0 {
			stack.StepResult[resultKey] = jsonInRspBody
		}
		return jsonInRspBody, http.StatusOK
	}
}
