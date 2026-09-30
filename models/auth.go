package models

// AuthResponse হলো সেই JSON রেসপন্স যা আমরা লগইন বা টোকেন রিফ্রেশ করার পর ক্লায়েন্টকে দেব
type AuthResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	User         *User  `json:"user,omitempty"`
}

// RefreshRequest হলো ক্লায়েন্ট যখন নতুন Access Token চাইতে আসবে, তখন যে JSON বডি পাঠাবে
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type RegisterRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}
