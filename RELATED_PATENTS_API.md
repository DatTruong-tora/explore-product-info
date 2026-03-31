# Get Related Patents API

## Overview

This API finds patent application IDs related to a given invention text.

It accepts a long invention description in the request body, searches USPTO using several query variants, deduplicates the results, and returns the related patent IDs as `[]string`.

## Endpoint

`POST /api/v1/patents/related`

## Request Body

```json
{
  "invention_text": "portable bio-signal measuring device",
  "limit": 10
}
```

### Fields

- `invention_text`: Required. The invention text or invention idea to search for.
- `limit`: Optional. Number of patent IDs to return.
  - Default: `10`
  - Max: `50`

## Response

```json
{
  "status": "success",
  "data": {
    "invention_text": "portable bio-signal measuring device",
    "limit": 10,
    "patent_ids": [
      "US100",
      "US101",
      "US102"
    ]
  }
}
```

## How It Works

The service currently uses USPTO as the patent source.

For each request, it runs multiple USPTO search patterns:

- exact `inventionTitle:"..."`
- quoted phrase search
- keyword `AND` search built from important terms in the invention text

The service then:

1. collects all returned `applicationNumberText` values
2. removes duplicates
3. stops when the requested limit is reached

## Error Cases

- `400 Bad Request`
  - invalid JSON body
  - missing `invention_text`

- `500 Internal Server Error`
  - USPTO request failure
  - missing `USPTO_API_KEY`
  - upstream decoding or network errors

## Notes

- Returned values are patent application IDs, taken from USPTO field `applicationNumberText`.
- Results depend on USPTO search quality and the wording of the invention text.
- The API is designed for recall first, so multiple search variants are used to find as many related patent IDs as possible.
