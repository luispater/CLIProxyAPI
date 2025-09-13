package geminiwebapi

type RPCData struct {
    RPCID      GRPC
    Payload    string
    Identifier string
}

func (r RPCData) Serialize() []any {
    if r.Identifier == "" { r.Identifier = "generic" }
    return []any{string(r.RPCID), r.Payload, nil, r.Identifier}
}

