package geminiwebapi

import "net/http"

// Endpoints used by the Gemini web app
const (
    EndpointGoogle        = "https://www.google.com"
    EndpointInit          = "https://gemini.google.com/app"
    EndpointGenerate      = "https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"
    EndpointRotateCookies = "https://accounts.google.com/RotateCookies"
    EndpointUpload        = "https://content-push.googleapis.com/upload"
    EndpointBatchExec     = "https://gemini.google.com/_/BardChatUi/data/batchexecute"
)

// Default headers
var (
    HeadersGemini = http.Header{
        "Content-Type":   []string{"application/x-www-form-urlencoded;charset=utf-8"},
        "Host":           []string{"gemini.google.com"},
        "Origin":         []string{"https://gemini.google.com"},
        "Referer":        []string{"https://gemini.google.com/"},
        "User-Agent":     []string{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
        "X-Same-Domain":  []string{"1"},
    }
    HeadersRotateCookies = http.Header{
        "Content-Type": []string{"application/json"},
    }
    HeadersUpload = http.Header{
        "Push-ID": []string{"feeds/mcudyrk2a4khkz"},
    }
)

// GRPC identifiers used by the batch execute API
type GRPC string

const (
    GRPCReadChat  GRPC = "hNvQHb"
    GRPCListGems  GRPC = "CNgdBe"
    GRPCCreateGem GRPC = "oMH3Zd"
    GRPCUpdateGem GRPC = "kHv0Vd"
    GRPCDeleteGem GRPC = "UXcSJb"
)

// Model defines available model names and headers
type Model struct {
    Name         string
    ModelHeader  http.Header
    AdvancedOnly bool
}

var (
    ModelUnspecified = Model{
        Name:         "unspecified",
        ModelHeader:  http.Header{},
        AdvancedOnly: false,
    }
    ModelG25Flash = Model{
        Name: "gemini-2.5-flash",
        ModelHeader: http.Header{
            "x-goog-ext-525001261-jspb": []string{"[1,null,null,null,\"71c2d248d3b102ff\",null,null,0,[4]]"},
        },
        AdvancedOnly: false,
    }
    ModelG25Pro = Model{
        Name: "gemini-2.5-pro",
        ModelHeader: http.Header{
            "x-goog-ext-525001261-jspb": []string{"[1,null,null,null,\"4af6c7f5da75d65d\",null,null,0,[4]]"},
        },
        AdvancedOnly: false,
    }
    ModelG20Flash = Model{ // Deprecated, still supported
        Name: "gemini-2.0-flash",
        ModelHeader: http.Header{
            "x-goog-ext-525001261-jspb": []string{"[1,null,null,null,\"f299729663a2343f\"]"},
        },
        AdvancedOnly: false,
    }
    ModelG20FlashThinking = Model{ // Deprecated, still supported
        Name: "gemini-2.0-flash-thinking",
        ModelHeader: http.Header{
            "x-goog-ext-525001261-jspb": []string{"[null,null,null,null,\"7ca48d02d802f20a\"]"},
        },
        AdvancedOnly: false,
    }
)

// ModelFromName returns a model by name or error if not found
func ModelFromName(name string) (Model, error) {
    switch name {
    case ModelUnspecified.Name:
        return ModelUnspecified, nil
    case ModelG25Flash.Name:
        return ModelG25Flash, nil
    case ModelG25Pro.Name:
        return ModelG25Pro, nil
    case ModelG20Flash.Name:
        return ModelG20Flash, nil
    case ModelG20FlashThinking.Name:
        return ModelG20FlashThinking, nil
    default:
        return Model{}, &ValueError{Msg: "Unknown model name: " + name}
    }
}

// Known error codes returned from server
const (
    ErrorUsageLimitExceeded  = 1037
    ErrorModelInconsistent   = 1050
    ErrorModelHeaderInvalid  = 1052
    ErrorIPTemporarilyBlocked = 1060
)

