package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"time"
)

type SwaggerSpec struct {
	Paths       map[string]interface{}            `json:"paths"`
	Definitions map[string]map[string]interface{} `json:"definitions"`
}

type Parameter struct {
	In       string `json:"in"`
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Type     string `json:"type"`
	Format   string `json:"format"`
}

type MethodInfo struct {
	Route      string
	Method     string
	Parameters []Parameter
	Responses  map[string]interface{}
	Summary    string
}

type RouteMethods map[string]MethodInfo

type RouteMethodsList []MethodInfo

const maxConcurrency = 5

func fetchSwaggerSpec(url string) (*SwaggerSpec, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var spec SwaggerSpec
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if str, ok := v.(string); ok {
			return str
		}
	}
	return ""
}

func getBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func parsePaths(rawPaths map[string]interface{}) RouteMethodsList {
	routes := map[string]RouteMethods{}

	methods := RouteMethodsList{}

	for route, methodsRaw := range rawPaths {
		methodsMap, ok := methodsRaw.(map[string]interface{})
		if !ok {
			continue
		}
		routeMethods := RouteMethods{}

		for methodName, methodRaw := range methodsMap {
			methodData, ok := methodRaw.(map[string]interface{})
			if !ok {
				continue
			}

			var params []Parameter
			if rawParams, exists := methodData["parameters"]; exists {
				if paramList, ok := rawParams.([]interface{}); ok {
					for _, p := range paramList {
						pMap, ok := p.(map[string]interface{})
						if !ok {
							continue
						}
						param := Parameter{
							In:       getString(pMap, "in"),
							Name:     getString(pMap, "name"),
							Required: getBool(pMap, "required"),
							Type:     getString(pMap, "type"),
							Format:   getString(pMap, "format"),
						}
						params = append(params, param)
					}
				}
			}

			responses := map[string]interface{}{}
			if respRaw, exists := methodData["responses"]; exists {
				responses, _ = respRaw.(map[string]interface{})
			}
			summary := getString(methodData, "summary")

			methodInfo := MethodInfo{
				Route:      route,
				Method:     methodName,
				Parameters: params,
				Responses:  responses,
				Summary:    summary,
			}

			methods = append(methods, methodInfo)

			// routeMethods[strings.ToLower(methodName)] = methodInfo
		}
		routes[route] = routeMethods
	}
	return methods
}

func debugMethod(info MethodInfo) {
	fmt.Printf("Route: %s\n", info.Route)
	fmt.Printf("  Method: %s\n", info.Method)
	// fmt.Printf("    Summary: %s\n", info.Summary)
	fmt.Printf("    Parameters:\n")
	for _, p := range info.Parameters {
		fmt.Printf("      - %s (%s), required: %v\n", p.Name, p.In, p.Required)
	}
	// fmt.Printf("    Responses:\n")
	// for code, resp := range info.Responses {
	// 	fmt.Printf("      %s: %v\n", code, resp)
	// }

}

func fanoutGoroutine(ctx context.Context) chan error {
	ticker := time.NewTicker(1 * time.Minute)
	errChan := make(chan error)
	go func() {
		defer ticker.Stop()
		defer close(errChan)
		for {
			select {
			case <-ticker.C:
				fmt.Println("deliver processed requests to postgres")
			case <-ctx.Done():
				fmt.Println("Shutting down ProduceDeleteExpiredCards goroutine.")
				return
			}
		}
	}()

	return errChan
}

func main() {

	// TODO: map + mutex to update common map from parsePaths
	swaggerURL := "http://localhost:8080/swagger.json"

	spec, err := fetchSwaggerSpec(swaggerURL)
	if err != nil {
		fmt.Printf("Error fetching Swagger spec: %v\n", err)
		return
	}

	parsedSpec := parsePaths(spec.Paths)
	methodsCh := make(chan MethodInfo)

	semaphore := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	go func() {
		for _, methodInf := range parsedSpec {

			wg.Add(1)
			go func(m MethodInfo) {
				// !!!! внутри горутины semaphore <- struct{}{}
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				defer wg.Done()
				// debugMethod(m)
				methodsCh <- m
			}(methodInf)
		}
		// for i := 0; i < maxConcurrency; i++ {
		// 	semaphore <- struct{}{}
		// }
		wg.Wait()
		close(methodsCh)
	}()

	// TODO: FAN-OUT goroutine to deliver processed requests to postgres
	for methodInfo := range methodsCh {
		baseURL := "http://localhost:8080/v2"
		url := baseURL + methodInfo.Route

		var bodyData interface{}
		for _, param := range methodInfo.Parameters {
			if param.In == "body" {
				bodyData = generateSampleDataFromSchema(resolveRef(param.Name, spec.Definitions))
			}
		}

		var payload []byte
		if bodyData != nil {
			fmt.Printf("bodyData: %v\n\n", bodyData)
			payload, err = json.Marshal(bodyData)
			if err != nil {
				fmt.Printf("Error marshaling request data for %s: %v", payload, err)
				continue
			}
		}

		req, err := http.NewRequest(methodInfo.Method, url, bytes.NewReader(payload))
		if err != nil {
			fmt.Printf("Error creating request for %s %s: %v", methodInfo.Method, url, err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		// Send the request
		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("Request failed for %s %s: %v", methodInfo.Method, url, err)
			continue
		}
		// Read response
		respBody, err := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			fmt.Printf("Error reading response for %s %s: %v", methodInfo.Method, url, err)
			continue
		}
		fmt.Printf("Response for %s %s: %s\n\n\n", methodInfo.Method, url, string(respBody))

	}

}
func resolveRef(ref string, schemas map[string]map[string]interface{}) map[string]interface{} {
	lowerSearchKey := strings.ToLower(ref)
	for k, v := range schemas {
		if strings.ToLower(k) == lowerSearchKey {
			return v
		}
	}
	return nil
}

func generateSampleDataFromSchema(schema map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return result
	}

	for propName, propSchema := range properties {
		propMap, ok := propSchema.(map[string]interface{})
		if !ok {
			continue
		}

		t, _ := propMap["type"].(string)
		f, _ := propMap["format"].(string)
		switch t {
		case "string":
			switch f {
			case "date-time":
				result[propName] = "2023-10-10T10:00:00Z"
			case "date":
				result[propName] = "2023-10-10"
			case "uuid":
				result[propName] = "123e4567-e89b-12d3-a456-426614174000"
			default:
				result[propName] = "sample string"
			}
		case "integer":
			result[propName] = 1
		case "number":
			result[propName] = 1.23
		case "boolean":
			result[propName] = true
		case "array":
			items, ok := propMap["items"].(map[string]interface{})
			if ok {
				result[propName] = []interface{}{generateSampleDataFromSchema(items)}
			} else {
				result[propName] = []interface{}{}
			}
		case "object":
			result[propName] = generateSampleDataFromSchema(propMap)
		default:
			result[propName] = nil
		}
	}

	return result
}
