package app

import (
	"log"
	"net/http"
	"time"

	"github.com/campbellcharlie/lorg/internal/types"
	"github.com/labstack/echo/v4"
)

type RepeaterSendRequest struct {
	Host        string  `json:"host"`
	Port        string  `json:"port"`
	TLS         bool    `json:"tls"`
	Request     string  `json:"request"`
	Timeout     float64 `json:"timeout"`
	HTTP2       bool    `json:"http2"`
	Index       float64 `json:"index"`
	Url         string  `json:"url"`
	GeneratedBy string  `json:"generated_by"`
	Note        string  `json:"note,omitempty"`
}

type RepeaterSendResponse struct {
	Response string         `json:"response"`
	Time     string         `json:"time"`
	UserData types.UserData `json:"userdata"`
}

// sendRepeaterLogic contains the core logic for sending a raw HTTP request and saving to backend.
func (backend *Backend) sendRepeaterLogic(reqData *RepeaterSendRequest) (*RepeaterSendResponse, error) {
	timeout := time.Duration(reqData.Timeout) * time.Second
	respString, timeTaken, err := SendRawHTTPRequest(
		reqData.Host,
		reqData.Port,
		reqData.TLS,
		reqData.Request,
		timeout,
		reqData.HTTP2,
	)

	if err != nil {
		return nil, err
	}

	addReqBody := types.AddRequestBodyType{
		Url:         reqData.Url,
		Index:       reqData.Index,
		Request:     reqData.Request,
		Response:    respString,
		GeneratedBy: "repeater/" + reqData.GeneratedBy,
		Note:        reqData.Note,
	}

	userdata, err := backend.SaveRequestToBackend(addReqBody)
	if err != nil {
		log.Printf("[sendRepeaterLogic] Error saving to backend: %v", err)
		// Still return the response even if save fails
		return &RepeaterSendResponse{
			Response: respString,
			Time:     timeTaken,
		}, nil
	}

	return &RepeaterSendResponse{
		Response: respString,
		Time:     timeTaken,
		UserData: userdata,
	}, nil
}

// SendRepeater handles the /api/repeater/send endpoint
func (backend *Backend) SendRepeater(e *echo.Echo) {
	e.POST("/api/repeater/send", func(c echo.Context) error {
		log.Println("[SendRepeater] Handler called")

		if err := requireLocalhost(c); err != nil {
			return err
		}

		var reqData RepeaterSendRequest
		if err := c.Bind(&reqData); err != nil {
			log.Printf("[SendRepeater] Error binding body: %v", err)
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		}

		log.Printf("[SendRepeater] Request data: %+v", reqData)

		resp, err := backend.sendRepeaterLogic(&reqData)
		if err != nil {
			log.Printf("[SendRepeater] Error sending request: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}

		log.Printf("[SendRepeater] Successfully processed request")
		return c.JSON(http.StatusOK, resp)
	})
}
