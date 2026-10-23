package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/service"
)

type parameterTranslationProgressKey struct{}

func parameterTranslationLog(context aiLogContext, translation *service.ParameterTranslation) aiLogContext {
	context.OutcomeKnown = true
	request := translation.Submission
	if request == nil {
		return context
	}
	context.Method, context.Endpoint = request.Method, request.URL.String()
	if request.GetBody != nil {
		reader, err := request.GetBody()
		if err == nil {
			body, _ := io.ReadAll(reader)
			_ = reader.Close()
			context.RequestBody = summarizeAIRequest(body, request.Header.Get("Content-Type"))
		}
	}
	if key := context.Channel.APIKey; key != "" {
		encoded, _ := json.Marshal(key)
		for _, value := range []string{key, string(encoded[1 : len(encoded)-1]), url.QueryEscape(key), url.PathEscape(key)} {
			context.Endpoint = strings.ReplaceAll(context.Endpoint, value, "[密钥]")
			context.RequestBody = strings.ReplaceAll(context.RequestBody, value, "[密钥]")
		}
	}
	return context
}

func serveParameterTranslation(w http.ResponseWriter, r *http.Request, body []byte, path string, channel model.ModelChannel, user model.AuthUser, credits float64, startedAt time.Time) bool {
	input, err := service.ReadParameterTranslationInput(body)
	if input == nil && err == nil {
		return false
	}
	if err == nil && ((input.Kind == "image" && path != "/images/generations" && path != "/images/edits") || (input.Kind == "audio" && path != "/audio/speech") || input.Kind == "video") {
		Fail(w, "自定义调用类型与接口不一致")
		return true
	}
	var request struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &request)
	var translation *service.ParameterTranslation
	if err == nil {
		translation, err = service.NewParameterTranslation(channel, request.Model, input.Variables)
	}
	if err != nil {
		Fail(w, err.Error())
		return true
	}
	if translation == nil {
		Fail(w, "当前渠道没有该模型的自定义配置，请刷新渠道设置")
		return true
	}
	if r.Header.Get("X-Canvas-Node-Request") == "true" && r.Context().Err() != nil {
		return true
	}
	if credits > 0 {
		if err := service.ConsumeUserCredits(user.ID, request.Model, credits, path); err != nil {
			FailError(w, err)
			return true
		}
	}
	logContext := aiLogContext{StartedAt: startedAt, Endpoint: path, Method: http.MethodPost, Model: request.Model, Channel: channel, UserID: user.ID, UserDisplayName: firstNonEmpty(user.DisplayName, user.Username), Credits: credits, RequestBody: summarizeAIRequest(body, "application/json")}
	onProgress, _ := r.Context().Value(parameterTranslationProgressKey{}).(func(int))
	result, err := translation.Execute(r.Context(), input.Kind, onProgress)
	logContext = parameterTranslationLog(logContext, translation)
	if err != nil {
		if credits > 0 && (r.Header.Get("X-Canvas-Node-Request") != "true" || r.Context().Err() == nil) {
			_ = service.RefundUserCredits(user.ID, request.Model, credits, path)
		}
		saveAIProxyLog(logContext, result.StatusCode, result.ResponseBody, err.Error())
		Fail(w, err.Error())
		return true
	}
	saveAIProxyLog(logContext, result.StatusCode, result.ResponseBody, "")
	if len(result.Body) > 0 {
		w.Header().Set("Content-Type", result.ContentType)
		_, _ = w.Write(result.Body)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	if input.Kind == "audio" {
		_ = json.NewEncoder(w).Encode(map[string]any{"provider": "parameter-translation", "audio_url": result.URLs[0]})
		return true
	}
	images := make([]map[string]string, 0, len(result.URLs))
	for _, url := range result.URLs {
		images = append(images, map[string]string{"url": url})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": images})
	return true
}

func serveParameterTranslationVideo(w http.ResponseWriter, r *http.Request, body []byte, channel model.ModelChannel, user model.AuthUser, credits float64, startedAt time.Time) bool {
	input, err := service.ReadParameterTranslationInput(body)
	if input == nil && err == nil {
		return false
	}
	if err == nil && input.Kind != "video" {
		Fail(w, "自定义调用类型与视频接口不一致")
		return true
	}
	var request struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &request)
	var translation *service.ParameterTranslation
	if err == nil {
		translation, err = service.NewParameterTranslation(channel, request.Model, input.Variables)
	}
	if err != nil {
		Fail(w, err.Error())
		return true
	}
	if translation == nil {
		Fail(w, "当前渠道没有该模型的自定义配置，请刷新渠道设置")
		return true
	}
	if credits > 0 {
		if err := service.ConsumeUserCredits(user.ID, request.Model, credits, "/videos"); err != nil {
			FailError(w, err)
			return true
		}
	}
	logContext := aiLogContext{StartedAt: startedAt, Endpoint: "/videos", Method: http.MethodPost, Model: request.Model, Channel: channel, UserID: user.ID, UserDisplayName: firstNonEmpty(user.DisplayName, user.Username), Credits: credits, RequestBody: summarizeAIRequest(body, "application/json")}
	result, err := translation.Send(r.Context(), "video", false, "")
	logContext = parameterTranslationLog(logContext, translation)
	if err != nil {
		if id := readClientVideoTaskID(r); id != "" {
			if task, found, _ := service.GetUserVideoTask(user.ID, id); found && task.Status == "cancelled" {
				Fail(w, "任务已停止")
				return true
			}
		}
		if credits > 0 {
			refundVideoCredits(user.ID, request.Model, credits, "/videos")
		}
		saveAIProxyLog(logContext, result.StatusCode, result.ResponseBody, err.Error())
		Fail(w, err.Error())
		return true
	}
	videoURL := ""
	if len(result.URLs) > 0 {
		videoURL = result.URLs[0]
	}
	userChannelID := r.Header.Get(userModelChannelHeader)
	seconds, size := "", ""
	if value, ok := input.Variables["seconds"]; ok {
		seconds = fmt.Sprint(value)
	}
	if value, ok := input.Variables["size"]; ok {
		size = fmt.Sprint(value)
	}
	task, err := service.CreateVideoTask(service.VideoTaskCreateInput{
		UserID: user.ID, UserDisplayName: logContext.UserDisplayName, Model: request.Model, ChannelID: channel.ID, UserChannelID: userChannelID, ChannelName: channel.Name,
		Source: readVideoTaskSource(r), SourceID: readVideoTaskSourceID(r), ClientTaskID: readClientVideoTaskID(r), Status: result.Status, Progress: result.Progress,
		Seconds: seconds, Size: size,
		VideoURL: videoURL, RequestBody: string(body), ResponseBody: result.ResponseBody, Credits: credits, ParameterTranslationSnapshot: translation.Snapshot(*input, result),
	})
	if err != nil {
		if credits > 0 {
			refundVideoCredits(user.ID, request.Model, credits, "/videos")
		}
		Fail(w, "视频任务保存失败")
		return true
	}
	saveAIProxyLog(logContext, result.StatusCode, result.ResponseBody, "")
	if len(result.Body) > 0 {
		w.Header().Set("Content-Type", result.ContentType)
		_, _ = w.Write(result.Body)
		return true
	}
	OK(w, service.VideoTaskResponse(task))
	return true
}
