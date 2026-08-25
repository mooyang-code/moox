export interface HealthAlert { id?: string; title?: string; status?: string; reason?: string; checked_at?: string; severity?: string }
export interface HealthInstance { name?: string; node_id?: string; instance_id?: string; status?: string; conclusion?: string; last_checked_at?: string }
export interface HealthItem { id?: string; group?: string; name?: string; description?: string; status?: string; reason?: string; checked_at?: string; omitted_instance_count?: number; instances?: HealthInstance[] }
export interface HealthOverview { generated_at?: string; alerts?: HealthAlert[]; business_items?: HealthItem[]; service_items?: HealthItem[]; notification_channel_type?: string; notification_configured?: boolean; notification_webhook_masked?: string }
export interface NotificationChannelSetting { channel_type?: string; configured?: boolean; masked_url?: string; updated_at?: string }
export interface NotificationChannelResponse { channel?: NotificationChannelSetting }
