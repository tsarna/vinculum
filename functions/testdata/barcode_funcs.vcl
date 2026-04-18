// End-to-end smoke tests for barcode-cty-func integration.

const {
    qr_default  = barcode("qr", "https://example.com")
    qr_scaled   = barcode("qr", "hello", { scale = 8 })
    qr_sized    = barcode("qr", "hello", { width = 200, height = 200 })
    qr_ec       = barcode("qr", "hello", { error_correction = "H" })
    code128_img = barcode("code128", "HELLO123")
    ean13_img   = barcode("ean13", "590123412345")
}

assert "qr_default_content_type" {
    condition = (qr_default.content_type == "image/png")
}

assert "qr_scaled_content_type" {
    condition = (qr_scaled.content_type == "image/png")
}

assert "qr_sized_content_type" {
    condition = (qr_sized.content_type == "image/png")
}

assert "qr_ec_content_type" {
    condition = (qr_ec.content_type == "image/png")
}

assert "code128_content_type" {
    condition = (code128_img.content_type == "image/png")
}

assert "ean13_content_type" {
    condition = (ean13_img.content_type == "image/png")
}

assert "qr_has_data" {
    condition = (length(qr_default) > 0)
}
