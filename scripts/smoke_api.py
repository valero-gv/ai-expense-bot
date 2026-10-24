#!/usr/bin/env python3
"""Real API CRUD smoke test. No third-party packages; cleans up created expenses."""
import json
import os
import urllib.error
import urllib.parse
import urllib.request

BASE = os.environ.get("BASE_URL", "http://127.0.0.1:8080").rstrip("/")
# Legacy Russian category keys are stored API values, not user-facing demo text.
LEGACY_CATEGORY_FOOD = "еда"
LEGACY_CATEGORY_TRANSPORT = "транспорт"


def request(method, path, body=None, expected=200):
    headers = {"Content-Type": "application/json"} if body is not None else {}
    data = json.dumps(body, ensure_ascii=False).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    assert status == expected, (method, path, status, raw.decode())
    return json.loads(raw) if raw else None


def main():
    created = []
    try:
        assert request("GET", "/healthz")["status"] == "ok"
        assert len(request("GET", "/api/categories")["items"]) == 7
        for amount in ("0.10", "0.20"):
            row = request("POST", "/api/expenses", {
                "user_id": 900003, "amount": amount, "category": LEGACY_CATEGORY_FOOD,
                "spent_at": "2026-10-16", "comment": "api-smoke-exact-money",
            }, 201)
            created.append(row["id"])
        query = urllib.parse.urlencode({"user_id": 900003, "category": LEGACY_CATEGORY_FOOD, "from": "2026-10-16", "to": "2026-10-16", "q": "api-smoke-exact-money"})
        rows = request("GET", "/api/expenses?" + query)
        assert rows["total"] == 2
        assert rows["items"][0]["id"] == created[-1]
        assert request("GET", "/api/summary?" + query) == {"amount": "0.30", "count": 2}
        updated = request("PUT", f"/api/expenses/{created[0]}", {
            "user_id": 900003, "amount": "450.25", "category": LEGACY_CATEGORY_TRANSPORT,
            "spent_at": "2026-10-15", "comment": "api-smoke-edited",
        })
        assert updated["amount"] == "450.25"
        assert request("GET", f"/api/expenses/{created[0]}")["spent_at"] == "2026-10-15"
        request("POST", "/api/expenses", {"user_id": 1, "amount": 0.1}, 400)
        request("DELETE", f"/api/expenses/{created[0]}", expected=204)
        request("GET", f"/api/expenses/{created[0]}", expected=404)
        created.pop(0)
        print("PASS: health, categories, create, exact 0.10+0.20, filters, sort, update, delete, validation")
    finally:
        for row_id in created:
            request("DELETE", f"/api/expenses/{row_id}", expected=204)


if __name__ == "__main__":
    main()
