package main

import (
	_ "github.com/lib/pq"
)

type EmbeddingRequest struct {
	Model	string	`json:"model"`
	Prompt	string	`json:"prompt"`
}

type EmbeddingResponse struct {
	Embedding []float64	`json:"embedding"`
}

func getEmbedding(text string) ([]float64, error) {
	reqBody := EmbeddingRequest{
		Model: "nomic-embed-text",
		Prompt: text,
	}
}
