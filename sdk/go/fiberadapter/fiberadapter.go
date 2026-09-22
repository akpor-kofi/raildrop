package fiberadapter

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gofiber/fiber/v2"
)

func toHTTPRequest(c *fiber.Ctx) (*http.Request, error) {
	fastRequest := c.Request()
	uri := fastRequest.URI()
	target := string(uri.PathOriginal())
	if query := string(uri.QueryString()); query != "" {
		target += "?" + query
	}
	request, err := http.NewRequest(string(fastRequest.Header.Method()), target, bytes.NewReader(fastRequest.Body()))
	if err != nil {
		return nil, err
	}
	fastRequest.Header.VisitAll(func(key []byte, value []byte) {
		request.Header.Add(string(key), string(value))
	})
	request.Host = string(uri.Host())
	request.URL.Host = string(uri.Host())
	if string(uri.Scheme()) == "https" {
		request.URL.Scheme = "https"
	}
	return request, nil
}

func Handler(handler http.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		request, err := toHTTPRequest(c)
		if err != nil {
			return fiber.ErrBadRequest
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		result := recorder.Result()
		defer result.Body.Close()
		c.Status(result.StatusCode)
		for name, values := range result.Header {
			for _, value := range values {
				c.Set(name, value)
			}
		}
		body, err := io.ReadAll(result.Body)
		if err != nil {
			return fiber.ErrInternalServerError
		}
		return c.Send(body)
	}
}

func Register(app *fiber.App, path string, handler http.Handler) *fiber.App {
	app.All(path, Handler(handler))
	return app
}
