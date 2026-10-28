<?php
declare(strict_types=1);

/**
 * Skeleton webhook for Mirza panel (botmirzapanel).
 * Place it next to the panel, e.g. /payment/bluey/callback.php.
 *
 * Configure the panel to send callback_url to this file's public URL.
 * Set WEBHOOK_SECRET in the PHP environment (same value as bluey).
 */

$secret = getenv('WEBHOOK_SECRET') ?: '';
$raw = file_get_contents('php://input');
$signature = $_SERVER['HTTP_X_RECEIPT_SIGNATURE'] ?? '';

if ($secret === '' || !hash_equals('sha256=' . hash_hmac('sha256', $raw, $secret), $signature)) {
    http_response_code(401);
    exit('bad signature');
}

$event = json_decode($raw, true);
if (!is_array($event) || ($event['type'] ?? '') !== 'payment.paid') {
    http_response_code(204);
    exit;
}

$orderId = (string)($event['data']['order_id'] ?? '');
if ($orderId === '') {
    http_response_code(400);
    exit('missing order_id');
}

// TODO: load your Mirza version's bootstrap.
// require __DIR__ . '/../../config.php';
// require __DIR__ . '/../../functions.php';

// TODO: make it idempotent. Example:
// $row = $pdo->prepare("SELECT payment_Status FROM Payment_report WHERE id_order = ? LIMIT 1");
// $row->execute([$orderId]);
// if ($row->fetchColumn() === 'paid') { http_response_code(200); exit('already paid'); }

// Credit the wallet / create the config, exactly like the admin approval button.
// DirectPayment($orderId);

// TODO: mark the report as paid (column/table names differ between versions).
// $pdo->prepare("UPDATE Payment_report SET payment_Status = 'paid' WHERE id_order = ?")->execute([$orderId]);

http_response_code(200);
echo 'ok';
